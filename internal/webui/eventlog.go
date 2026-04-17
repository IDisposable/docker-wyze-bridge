package webui

import (
	"sync"
	"time"
)

// Event is a single item in the rolling event log the /metrics page
// renders. Events represent "things that happened" — state changes,
// recording start/stop, error spikes — not process-wide metrics
// (those read live from their owning subsystems).
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`    // "state", "record", "error", "discovery"
	Camera  string    `json:"camera"`  // optional — empty for bridge-wide events
	Message string    `json:"message"` // human-readable summary
	Detail  string    `json:"detail,omitempty"`
}

// EventLog is a fixed-size ring buffer of recent events, goroutine-safe.
// In-memory only by design; persisting would require disk I/O on every
// state transition for observability information that's already on the
// MQTT topic + logs.
type EventLog struct {
	mu    sync.Mutex
	cap   int
	items []Event // newest last
}

// NewEventLog returns a ring buffer holding at most cap entries.
func NewEventLog(cap int) *EventLog {
	if cap < 1 {
		cap = 100
	}
	return &EventLog{cap: cap, items: make([]Event, 0, cap)}
}

// Record appends an event, evicting the oldest if at capacity. Time is
// stamped automatically if the caller left it zero.
func (l *EventLog) Record(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) == l.cap {
		// Drop the oldest — shift the slice down. Ring buffer with
		// a head/tail index would avoid the copy, but at cap=100
		// the shift is trivial and the slice-order semantics make
		// the Snapshot method zero-allocation.
		copy(l.items, l.items[1:])
		l.items = l.items[:l.cap-1]
	}
	l.items = append(l.items, e)
}

// Snapshot returns the events newest-first.
func (l *EventLog) Snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.items))
	// Reverse copy so callers see newest-first without having to sort.
	for i, e := range l.items {
		out[len(l.items)-1-i] = e
	}
	return out
}

// Len returns the current number of stored events.
func (l *EventLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}
