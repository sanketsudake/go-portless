package k8s

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

type fakeDialer struct {
	mu    sync.Mutex
	calls int
	conns []*fakeConn
	err   error
}

func (d *fakeDialer) dial(ctx context.Context, t target) (pooledConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	c := &fakeConn{port: t.containerPort}
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *fakeDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type fakeConn struct {
	port       int
	dead       bool
	failStream bool
	mu         sync.Mutex
}

func (c *fakeConn) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.dead
}
func (c *fakeConn) containerPortNum() int { return c.port }
func (c *fakeConn) close() {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()
}
func (c *fakeConn) dialStream() (net.Conn, error) {
	c.mu.Lock()
	fail := c.failStream
	c.mu.Unlock()
	if fail {
		return nil, errors.New("stream reset: pod gone")
	}
	client, server := net.Pipe()
	server.Close()
	return client, nil
}

func newTestBackend(t *testing.T, dialer streamDialer, objs ...runtime.Object) *portForward {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	pf := &portForward{
		res:    &resolver{client: client, opts: options{namespace: "fission", selector: "app=router", targetPort: intstr.FromInt32(8888), hasTarget: true}},
		dialer: dialer,
	}
	if err := pf.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Stop(context.Background()) })
	return pf
}

func TestBackendCachesConnection(t *testing.T) {
	fd := &fakeDialer{}
	pf := newTestBackend(t, fd, readyPod("router-1", "fission", map[string]string{"app": "router"}))

	for range 3 {
		c, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if got := fd.callCount(); got != 1 {
		t.Fatalf("dialer called %d times, want 1 (connection should be cached)", got)
	}
}

func TestBackendSelfHealsOnDeadConn(t *testing.T) {
	fd := &fakeDialer{}
	var events []portless.EventType
	var emu sync.Mutex
	pf := newTestBackend(t, fd, readyPod("router-1", "fission", map[string]string{"app": "router"}))
	pf.SetEventSink(func(e portless.Event) {
		emu.Lock()
		events = append(events, e.Type)
		emu.Unlock()
	})

	// First dial establishes and caches conn 0.
	c1, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	if fd.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", fd.callCount())
	}

	// Simulate the pod dying: the cached conn now fails to open streams.
	fd.conns[0].failStream = true
	_, err = pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if err == nil || !portless.IsRetryable(err) {
		t.Fatalf("dead-conn dial err = %v, want retryable", err)
	}

	// Next dial must re-resolve and re-dial (conn dropped).
	c3, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if err != nil {
		t.Fatalf("self-heal dial failed: %v", err)
	}
	c3.Close()
	if got := fd.callCount(); got != 2 {
		t.Fatalf("dialer called %d times, want 2 (re-dial after drop)", got)
	}

	emu.Lock()
	defer emu.Unlock()
	if !slices.Contains(events, portless.EventBackendUnhealthy) {
		t.Fatalf("expected EventBackendUnhealthy, got %v", events)
	}
}

func TestBackendNotReadyPodIsRetryable(t *testing.T) {
	fd := &fakeDialer{}
	pf := newTestBackend(t, fd, notReadyPod("router-0", "fission", map[string]string{"app": "router"}))

	_, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if err == nil || !portless.IsRetryable(err) {
		t.Fatalf("not-ready dial err = %v, want retryable", err)
	}
	if fd.callCount() != 0 {
		t.Fatalf("dialer should not be called when no pod is ready, calls = %d", fd.callCount())
	}
}

func TestMissingServiceIsTargetNotFoundThroughRegistry(t *testing.T) {
	client := fake.NewSimpleClientset() // no Service, no pods
	pf := &portForward{
		res:    &resolver{client: client, opts: options{namespace: "fission", service: "router"}},
		dialer: &fakeDialer{},
	}
	if err := pf.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Stop(context.Background()) })

	// Directly: the dial error is both retryable (late creation self-heals)
	// and a typed not-found (skip-fast callers detect it in one round-trip).
	_, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if !portless.IsRetryable(err) {
		t.Fatalf("missing-service dial err = %v, want retryable", err)
	}
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("missing-service dial err = %v, want ErrTargetNotFound", err)
	}

	// Through the registry: the readiness wait must preserve the sentinel
	// (dialWaitError wraps the last backend error).
	reg := portless.New()
	defer reg.Close()
	if _, err := reg.Add(context.Background(), "router.fission", pf,
		portless.RouteWithReadyTimeout(200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = reg.DialContext(context.Background(), "tcp", "router.fission:80")
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("registry dial err = %v, want errors.Is ErrTargetNotFound through the wait error", err)
	}
}

func TestServiceCreatedMidWaitRecovers(t *testing.T) {
	client := fake.NewSimpleClientset() // target appears later
	pf := &portForward{
		res:    &resolver{client: client, opts: options{namespace: "fission", selector: "app=router", targetPort: intstr.FromInt32(8888), hasTarget: true}},
		dialer: &fakeDialer{},
	}
	if err := pf.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Stop(context.Background()) })

	reg := portless.New()
	defer reg.Close()
	if _, err := reg.Add(context.Background(), "router.fission", pf,
		portless.RouteWithReadyTimeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		pod := readyPod("router-1", "fission", map[string]string{"app": "router"})
		if _, err := client.CoreV1().Pods("fission").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Error(err)
		}
	}()

	start := time.Now()
	c, err := reg.DialContext(context.Background(), "tcp", "router.fission:80")
	if err != nil {
		t.Fatalf("dial should recover once the target exists: %v", err)
	}
	c.Close()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("dial returned before the target was created")
	}
}

func TestBackendStopClosesConn(t *testing.T) {
	fd := &fakeDialer{}
	pf := newTestBackend(t, fd, readyPod("router-1", "fission", map[string]string{"app": "router"}))
	c, err := pf.DialContext(context.Background(), "tcp", "router.fission:80")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	pf.Stop(context.Background())
	if fd.conns[0].alive() {
		t.Fatal("Stop should close the cached connection")
	}
	if _, err := pf.DialContext(context.Background(), "tcp", "router.fission:80"); err == nil {
		t.Fatal("dial after Stop must fail")
	}
}
