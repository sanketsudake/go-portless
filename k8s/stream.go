package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// streamDialer opens a live multiplexed connection to a pod's port-forward
// subresource. It is an interface so the SPDY implementation can be swapped
// (e.g. for the WebSocket transport, KEP-4006) or faked in tests.
type streamDialer interface {
	dial(ctx context.Context, t target) (pooledConn, error)
}

// pooledConn is one live connection to a pod that the backend caches and
// reuses. Each dialStream opens a fresh forwarding stream over it.
type pooledConn interface {
	alive() bool
	dialStream() (net.Conn, error)
	containerPortNum() int
	close()
}

// spdyDialer dials the pod portforward subresource over SPDY.
type spdyDialer struct {
	cfg *rest.Config
}

func newSPDYDialer(cfg *rest.Config) streamDialer { return &spdyDialer{cfg: cfg} }

func (d *spdyDialer) dial(ctx context.Context, t target) (pooledConn, error) {
	transport, upgrader, err := spdy.RoundTripperFor(d.cfg)
	if err != nil {
		return nil, fmt.Errorf("spdy roundtripper: %w", err)
	}
	host, err := hostFromConfig(d.cfg)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", t.namespace, t.pod)
	u := host + path
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, mustParseURL(u))
	conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("dial portforward: %w", err)
	}
	return &pfConn{conn: conn, podRef: t.namespace + "/" + t.pod, containerPort: t.containerPort}, nil
}

// pfConn is a live SPDY connection to one pod. Each dialStream call opens a
// fresh error+data stream pair and returns the data stream as a net.Conn.
type pfConn struct {
	conn          httpstream.Connection
	podRef        string
	containerPort int
	requestID     atomic.Int64
}

func (c *pfConn) alive() bool {
	select {
	case <-c.conn.CloseChan():
		return false
	default:
		return true
	}
}

func (c *pfConn) close() { _ = c.conn.Close() }

func (c *pfConn) containerPortNum() int { return c.containerPort }

// dialStream opens a forwarding stream to the container port and returns it
// as a net.Conn. The error stream is drained in the background; a message on
// it (the pod died mid-stream) closes the data conn.
func (c *pfConn) dialStream() (net.Conn, error) {
	id := c.requestID.Add(1)
	portStr := strconv.Itoa(c.containerPort)

	headers := http.Header{}
	headers.Set(corev1.PortHeader, portStr)
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatInt(id, 10))

	// Error stream first, matching kubectl's ordering.
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	errStream, err := c.conn.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("create error stream: %w", err)
	}

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := c.conn.CreateStream(headers)
	if err != nil {
		c.conn.RemoveStreams(errStream)
		return nil, fmt.Errorf("create data stream: %w", err)
	}

	sc := &streamConn{
		conn:       c.conn,
		data:       dataStream,
		errStream:  errStream,
		localAddr:  pfAddr("portforward:local"),
		remoteAddr: pfAddr(fmt.Sprintf("portforward:%s:%d", c.podRef, c.containerPort)),
	}
	go sc.drainError()
	return sc, nil
}

// streamConn adapts an httpstream data stream to net.Conn. httpstream.Stream
// has no deadlines, so the deadline setters are honored via a monitor
// goroutine that closes the stream when a deadline elapses.
type streamConn struct {
	conn      httpstream.Connection
	data      httpstream.Stream
	errStream httpstream.Stream

	localAddr, remoteAddr net.Addr
	closeOnce             sync.Once
	errMsg                atomic.Pointer[string]
}

func (s *streamConn) Read(b []byte) (int, error) {
	n, err := s.data.Read(b)
	if err != nil {
		if msg := s.errMsg.Load(); msg != nil && *msg != "" {
			return n, fmt.Errorf("portforward: %s", *msg)
		}
	}
	return n, err
}

func (s *streamConn) Write(b []byte) (int, error) { return s.data.Write(b) }

func (s *streamConn) Close() error {
	s.closeOnce.Do(func() {
		s.conn.RemoveStreams(s.data, s.errStream)
		_ = s.data.Close()
		_ = s.errStream.Close()
	})
	return nil
}

func (s *streamConn) LocalAddr() net.Addr  { return s.localAddr }
func (s *streamConn) RemoteAddr() net.Addr { return s.remoteAddr }

// SetDeadline and friends: httpstream streams are not deadline-aware. Rather
// than silently ignore deadlines, a monitor goroutine closes the stream when
// the deadline passes, which unblocks any in-flight Read/Write.
func (s *streamConn) SetDeadline(t time.Time) error {
	s.armDeadline(t)
	return nil
}
func (s *streamConn) SetReadDeadline(t time.Time) error  { return s.SetDeadline(t) }
func (s *streamConn) SetWriteDeadline(t time.Time) error { return s.SetDeadline(t) }

func (s *streamConn) armDeadline(t time.Time) {
	if t.IsZero() {
		return
	}
	d := time.Until(t)
	if d <= 0 {
		_ = s.Close()
		return
	}
	time.AfterFunc(d, func() { _ = s.Close() })
}

// drainError reads the error stream; any content is a forwarding error from
// the kubelet (e.g. the pod went away) and closes the data conn.
func (s *streamConn) drainError() {
	msg, err := io.ReadAll(s.errStream)
	if err == nil && len(msg) > 0 {
		m := string(msg)
		s.errMsg.Store(&m)
		_ = s.Close()
	}
}

type pfAddr string

func (a pfAddr) Network() string { return "portforward" }
func (a pfAddr) String() string  { return string(a) }

var errNoHost = errors.New("k8s: rest.Config has no host")

func hostFromConfig(cfg *rest.Config) (string, error) {
	if cfg.Host == "" {
		return "", errNoHost
	}
	return cfg.Host, nil
}
