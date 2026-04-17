// Package issues is the bridge's observable error registry. Any
// subsystem that encounters a soft failure (bad env value, malformed
// template, unreachable dependency) reports it here instead of just
// logging. The /api/health and /metrics endpoints expose the current
// set so operators can see what's wrong without grepping logs, and
// so HA users get a config_errors binary sensor.
//
// Design notes:
//
//   - Issues are deduplicated by ID: repeated reports for the same
//     problem update LastSeen and bump Count rather than piling up.
//   - Resolve(id) removes an issue — subsystems call this when a
//     previously-reported problem fixes itself (e.g. reconnected to
//     a broker).
//   - The registry is safe for concurrent use.
//   - No fatal reporting — callers decide whether a reported issue
//     should also halt whatever they're doing. Issues are purely
//     observability.
package issues

import (
	"sort"
	"sync"
	"time"
)

// Severity classifies the impact of an issue. Metrics UIs render
// warnings differently from errors; HA can map severities to
// binary_sensor classes.
type Severity string

const (
	SeverityWarn  Severity = "warn"  // something's off but recoverable or cosmetic
	SeverityError Severity = "error" // a feature is degraded or disabled
)

// Issue is a single dedup-keyed report. Callers build one, call
// Registry.Report, and move on.
type Issue struct {
	// ID is the canonical deduplication key. Conventional format:
	// "<scope>/<camera>/<topic>" (camera empty for bridge-wide).
	// Same ID = same issue; subsequent Reports update timestamps and
	// Count rather than creating a new row.
	ID string

	Severity Severity
	Scope    string // e.g. "config", "record", "stream", "mqtt"
	Camera   string // optional; empty for bridge-wide issues

	// Message is the one-line summary rendered to users.
	Message string

	// Detail is the longer explanation, optionally multi-line.
	// Rendered in expanded/popup views.
	Detail string

	// RawValue holds the offending input (e.g. the env-var string
	// that failed to parse) for display. Callers should redact
	// anything sensitive before passing it in.
	RawValue string

	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

// Registry is a process-wide issue store. Construct with New,
// pass by pointer to subsystems that need to Report.
type Registry struct {
	mu     sync.RWMutex
	issues map[string]*Issue
	now    func() time.Time // injectable for tests
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		issues: map[string]*Issue{},
		now:    time.Now,
	}
}

// Report adds or updates an issue keyed by its ID. Severity, Scope,
// Camera, Message, Detail, RawValue are taken from the supplied issue
// on first sighting and refreshed on repeat reports — callers can
// update the message over time as context changes.
//
// Report is safe to call from any goroutine and is cheap enough that
// hot paths can invoke it without gating.
func (r *Registry) Report(iss Issue) {
	if iss.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if existing, ok := r.issues[iss.ID]; ok {
		existing.Count++
		existing.LastSeen = now
		// Refresh mutable fields — message may change as the caller
		// gathers more context; severity may escalate.
		existing.Severity = iss.Severity
		existing.Message = iss.Message
		existing.Detail = iss.Detail
		existing.RawValue = iss.RawValue
		existing.Scope = iss.Scope
		existing.Camera = iss.Camera
		return
	}
	iss.FirstSeen = now
	iss.LastSeen = now
	iss.Count = 1
	r.issues[iss.ID] = &iss
}

// Resolve removes an issue by ID. No-op if the ID isn't registered.
// Call this when a subsystem verifies its earlier-reported problem
// is no longer active (e.g. MQTT reconnected after a drop).
func (r *Registry) Resolve(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.issues, id)
}

// List returns a snapshot of all current issues, sorted by Severity
// (errors first) then by Scope + Camera + ID for stable UI rendering.
func (r *Registry) List() []Issue {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Issue, 0, len(r.issues))
	for _, iss := range r.issues {
		out = append(out, *iss)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			// errors before warnings
			return out[i].Severity == SeverityError
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		if out[i].Camera != out[j].Camera {
			return out[i].Camera < out[j].Camera
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Count returns the current number of distinct issues. Cheap to
// query from health endpoints that just want a "is everything OK"
// number.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.issues)
}
