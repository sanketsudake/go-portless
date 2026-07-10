package portless

import "time"

// EventType classifies registry and backend events.
type EventType int

const (
	// EventInvalid is the zero value, reserved so an accidentally zero-valued
	// Event is not mistaken for a real event type.
	EventInvalid EventType = iota
	// EventRouteAdded fires after a route is registered and its backend started.
	EventRouteAdded
	// EventRouteRemoved fires after a route is removed.
	EventRouteRemoved
	// EventDialStart fires when a dial enters a route's readiness loop.
	EventDialStart
	// EventDialRetry fires each time a not-ready backend dial is retried.
	EventDialRetry
	// EventDialSuccess fires when a route dial hands out a connection.
	EventDialSuccess
	// EventDialError fires when a route dial fails terminally.
	EventDialError
	// EventBackendUnhealthy is emitted by backends (via their event sink)
	// when they observe their endpoint failing, e.g. a pod restart.
	EventBackendUnhealthy
	// EventBackendRecovered is emitted by backends when the endpoint is back.
	EventBackendRecovered
)

// String returns the event type name.
func (t EventType) String() string {
	switch t {
	case EventInvalid:
		return "invalid"
	case EventRouteAdded:
		return "route_added"
	case EventRouteRemoved:
		return "route_removed"
	case EventDialStart:
		return "dial_start"
	case EventDialRetry:
		return "dial_retry"
	case EventDialSuccess:
		return "dial_success"
	case EventDialError:
		return "dial_error"
	case EventBackendUnhealthy:
		return "backend_unhealthy"
	case EventBackendRecovered:
		return "backend_recovered"
	default:
		return "unknown"
	}
}

// Event describes a routing occurrence. Handlers are invoked synchronously
// from the emitting goroutine and must be fast and non-blocking.
type Event struct {
	Type    EventType
	Route   string
	Address string        // the requested "name:port", when applicable
	Attempt int           // for EventDialRetry
	Err     error         // for EventDialRetry / EventDialError
	Elapsed time.Duration // for EventDialSuccess: total readiness wait
	Time    time.Time
}

// EventSinkSetter is an optional Backend capability: the Registry injects an
// event sink at Add time so backends can emit EventBackendUnhealthy/Recovered.
type EventSinkSetter interface {
	SetEventSink(func(Event))
}

// emit fans an event out to all registered handlers, stamping Time if unset.
func (r *Registry) emit(e Event) {
	if len(r.cfg.handlers) == 0 {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	for _, h := range r.cfg.handlers {
		h(e)
	}
}
