// Package k8s provides a portless Backend that forwards dials to a Kubernetes
// pod over an SPDY port-forward stream — one stream per dial, no local
// listener. It replaces fragile `kubectl port-forward` reconnect loops:
// pod restarts self-heal because each dial re-resolves a ready pod, and the
// registry's readiness loop absorbs the restart window.
//
// This lives in a separate module so the core go-portless module never
// depends on client-go.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Option configures a PortForward backend.
type Option func(*options)

type options struct {
	namespace  string
	service    string
	selector   string
	pod        string
	targetPort intstr.IntOrString
	hasTarget  bool
}

// Service resolves a ready pod behind the named Service and uses the
// Service's target port unless TargetPort overrides it.
func Service(namespace, name string) Option {
	return func(o *options) { o.namespace, o.service = namespace, name }
}

// LabelSelector resolves a ready pod matching selector (e.g. "app=router")
// within namespace. When several pods match, the FIRST ready one wins —
// there is no ambiguity error within a namespace.
func LabelSelector(namespace, selector string) Option {
	return func(o *options) { o.namespace, o.selector = namespace, selector }
}

// Pod targets a specific pod by name.
func Pod(namespace, name string) Option {
	return func(o *options) { o.namespace, o.pod = namespace, name }
}

// TargetPort sets the pod container port to forward to. It may be a number
// or a named port ("http"). Optional in all modes when a single port can be
// inferred: Service uses its one declared port; Pod/LabelSelector use the
// resolved pod's one declared container port. Required when the target
// declares zero or several ports.
func TargetPort(p intstr.IntOrString) Option {
	return func(o *options) { o.targetPort, o.hasTarget = p, true }
}

// PortForward returns a Backend that port-forwards to a pod resolved from cfg.
// It implements portless.Starter, portless.Stopper, and
// portless.EventSinkSetter.
func PortForward(cfg *rest.Config, opts ...Option) (portless.Backend, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.namespace == "" {
		return nil, errors.New("k8s: namespace is required")
	}
	if o.service == "" && o.selector == "" && o.pod == "" {
		return nil, errors.New("k8s: one of Service, LabelSelector, or Pod is required")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: build clientset: %w", err)
	}
	return &portForward{
		cfg:    cfg,
		res:    &resolver{client: client, opts: o},
		dialer: newSPDYDialer(cfg),
	}, nil
}

type portForward struct {
	cfg    *rest.Config
	res    *resolver
	dialer streamDialer

	mu       sync.Mutex
	conn     pooledConn // cached live connection to the current pod
	sink     func(portless.Event)
	closed   bool
	closedCh chan struct{}
}

// SetEventSink implements portless.EventSinkSetter.
func (p *portForward) SetEventSink(sink func(portless.Event)) {
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()
}

func (p *portForward) emit(e portless.Event) {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink(e)
	}
}

// Start implements portless.Starter. It only initializes shutdown signaling;
// the SPDY connection is established lazily on the first dial.
func (p *portForward) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closedCh == nil {
		p.closedCh = make(chan struct{})
	}
	return nil
}

// Stop implements portless.Stopper.
func (p *portForward) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.closedCh != nil {
		close(p.closedCh)
	}
	if p.conn != nil {
		p.conn.close()
		p.conn = nil
	}
	return nil
}

// DialContext resolves a ready pod (if needed) and opens a new forwarding
// stream to it. A not-ready pod or a dead connection returns a Retryable
// error so the registry's readiness loop waits and self-heals.
func (p *portForward) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("k8s: backend stopped")
	}
	p.mu.Unlock()

	conn, err := p.ensureConn(ctx)
	if err != nil {
		return nil, portless.Retryable(fmt.Errorf("k8s: connect to pod: %w", err))
	}
	c, err := conn.dialStream()
	if err != nil {
		// The cached connection is likely dead (pod restarted): drop it and
		// signal unhealthy so the next dial re-resolves.
		p.dropConn(conn)
		p.emit(portless.Event{Type: portless.EventBackendUnhealthy, Err: err, Time: time.Now()})
		return nil, portless.Retryable(fmt.Errorf("k8s: open stream: %w", err))
	}
	return c, nil
}

// ensureConn returns a live connection to a ready pod, resolving and dialing
// lazily and caching the result for reuse.
func (p *portForward) ensureConn(ctx context.Context) (pooledConn, error) {
	p.mu.Lock()
	if p.conn != nil && p.conn.alive() {
		conn := p.conn
		p.mu.Unlock()
		return conn, nil
	}
	p.mu.Unlock()

	target, err := p.res.resolve(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := p.dialer.dial(ctx, target)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.close()
		return nil, errors.New("k8s: backend stopped")
	}
	// Another dial may have raced us; keep the first live one.
	if p.conn != nil && p.conn.alive() {
		existing := p.conn
		p.mu.Unlock()
		conn.close()
		return existing, nil
	}
	if p.conn != nil {
		p.conn.close()
	}
	p.conn = conn
	p.mu.Unlock()

	// Emit outside the lock: emit re-acquires p.mu (non-reentrant).
	p.emit(portless.Event{Type: portless.EventBackendRecovered, Time: time.Now()})
	return conn, nil
}

func (p *portForward) dropConn(conn pooledConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == conn {
		p.conn.close()
		p.conn = nil
	}
}
