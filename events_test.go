package portless_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

type eventRecorder struct {
	mu     sync.Mutex
	events []portless.Event
}

func (er *eventRecorder) handle(e portless.Event) {
	er.mu.Lock()
	er.events = append(er.events, e)
	er.mu.Unlock()
}

func (er *eventRecorder) types() []portless.EventType {
	er.mu.Lock()
	defer er.mu.Unlock()
	out := make([]portless.EventType, len(er.events))
	for i, e := range er.events {
		out[i] = e.Type
	}
	return out
}

func (er *eventRecorder) has(t portless.EventType) bool {
	return slices.Contains(er.types(), t)
}

func TestEventsLifecycleAndDial(t *testing.T) {
	l := echoListener(t)
	er := &eventRecorder{}
	r := portless.New(portless.WithEventHandler(er.handle))
	defer r.Close()

	if _, err := r.Add("ev.test", backend.TCP(l.Addr().String())); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, r, "ev.test:80")
	if err := r.Remove(context.Background(), "ev.test"); err != nil {
		t.Fatal(err)
	}

	for _, want := range []portless.EventType{
		portless.EventRouteAdded, portless.EventDialStart,
		portless.EventDialSuccess, portless.EventRouteRemoved,
	} {
		if !er.has(want) {
			t.Fatalf("missing event %v in %v", want, er.types())
		}
	}
	// success event carries route + elapsed
	er.mu.Lock()
	defer er.mu.Unlock()
	for _, e := range er.events {
		if e.Type == portless.EventDialSuccess {
			if e.Route != "ev.test" || e.Elapsed < 0 || e.Time.IsZero() {
				t.Fatalf("bad success event: %+v", e)
			}
		}
	}
}

func TestEventsRetryAndError(t *testing.T) {
	er := &eventRecorder{}
	r := portless.New(portless.WithEventHandler(er.handle))
	defer r.Close()

	b := &notReadyBackend{} // never ready
	if _, err := r.Add("evr.test", b, portless.RouteWithReadyTimeout(150*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DialContext(context.Background(), "tcp", "evr.test:80"); err == nil {
		t.Fatal("expected dial failure")
	}
	if !er.has(portless.EventDialRetry) {
		t.Fatalf("missing EventDialRetry in %v", er.types())
	}
	if !er.has(portless.EventDialError) {
		t.Fatalf("missing EventDialError in %v", er.types())
	}
	er.mu.Lock()
	defer er.mu.Unlock()
	for _, e := range er.events {
		if e.Type == portless.EventDialRetry && e.Attempt < 1 {
			t.Fatalf("retry event should carry attempt >= 1: %+v", e)
		}
		if e.Type == portless.EventDialError && e.Err == nil {
			t.Fatalf("error event should carry err: %+v", e)
		}
	}
}

func TestMultipleEventHandlersFanOut(t *testing.T) {
	er1, er2 := &eventRecorder{}, &eventRecorder{}
	r := portless.New(portless.WithEventHandler(er1.handle), portless.WithEventHandler(er2.handle))
	defer r.Close()
	if _, err := r.Add("fan.test", backend.TCP("127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	if !er1.has(portless.EventRouteAdded) || !er2.has(portless.EventRouteAdded) {
		t.Fatal("both handlers must receive events")
	}
}

// sinkBackend emits backend events via the sink the registry injects.
type sinkBackend struct {
	backendFunc
	sink func(portless.Event)
}

func (b *sinkBackend) SetEventSink(sink func(portless.Event)) { b.sink = sink }

func TestBackendEventSinkWired(t *testing.T) {
	er := &eventRecorder{}
	r := portless.New(portless.WithEventHandler(er.handle))
	defer r.Close()

	b := &sinkBackend{backendFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("unused")
	}}
	if _, err := r.Add("sink.test", b); err != nil {
		t.Fatal(err)
	}
	if b.sink == nil {
		t.Fatal("registry must wire EventSinkSetter backends")
	}
	b.sink(portless.Event{Type: portless.EventBackendUnhealthy, Route: "sink.test"})
	if !er.has(portless.EventBackendUnhealthy) {
		t.Fatalf("backend-emitted event not fanned out: %v", er.types())
	}
}
