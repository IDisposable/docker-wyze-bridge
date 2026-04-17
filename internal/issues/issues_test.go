package issues

import (
	"testing"
	"time"
)

// TestRegistry_ReportDedup pins the core contract: repeated reports
// with the same ID don't create duplicates — they bump Count and
// refresh LastSeen. This is what makes "emit from a hot path" safe.
func TestRegistry_ReportDedup(t *testing.T) {
	r := New()
	clock := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return clock }

	r.Report(Issue{
		ID: "config/front_door/record_cmd", Severity: SeverityError,
		Scope: "config", Camera: "front_door", Message: "unknown token {unknwon}",
	})

	clock = clock.Add(5 * time.Minute)
	r.Report(Issue{
		ID: "config/front_door/record_cmd", Severity: SeverityError,
		Scope: "config", Camera: "front_door", Message: "unknown token {unknwon}",
	})

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("want 1 dedup'd issue, got %d", len(list))
	}
	iss := list[0]
	if iss.Count != 2 {
		t.Errorf("Count = %d, want 2", iss.Count)
	}
	if !iss.FirstSeen.Equal(time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("FirstSeen = %v, should not have moved on second report", iss.FirstSeen)
	}
	if !iss.LastSeen.Equal(clock) {
		t.Errorf("LastSeen = %v, should have advanced to second report time", iss.LastSeen)
	}
}

// TestRegistry_ReportRefreshesMutableFields — same ID, the second
// report can escalate severity or refine the message. Callers like
// a recording supervisor may want to escalate warn→error as retries
// accumulate.
func TestRegistry_ReportRefreshesMutableFields(t *testing.T) {
	r := New()
	r.Report(Issue{
		ID: "record/front_door/ffmpeg", Severity: SeverityWarn,
		Message: "ffmpeg exited, restarting",
	})
	r.Report(Issue{
		ID: "record/front_door/ffmpeg", Severity: SeverityError,
		Message: "ffmpeg crashed 5× in 30s — giving up",
	})
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("want 1 issue, got %d", len(list))
	}
	if list[0].Severity != SeverityError {
		t.Errorf("severity = %v, want escalated to error", list[0].Severity)
	}
	if list[0].Message != "ffmpeg crashed 5× in 30s — giving up" {
		t.Errorf("message = %q, should reflect the latest Report", list[0].Message)
	}
}

// TestRegistry_Resolve removes an issue by ID and is idempotent.
func TestRegistry_Resolve(t *testing.T) {
	r := New()
	r.Report(Issue{ID: "mqtt/broker", Severity: SeverityWarn, Message: "disconnected"})
	if r.Count() != 1 {
		t.Fatalf("setup failed")
	}
	r.Resolve("mqtt/broker")
	if r.Count() != 0 {
		t.Errorf("expected issue removed")
	}
	r.Resolve("mqtt/broker") // no-op; must not panic
	r.Resolve("nonexistent") // no-op; must not panic
}

// TestRegistry_EmptyIDIgnored guards against buggy callers silently
// polluting the registry with unkeyed entries.
func TestRegistry_EmptyIDIgnored(t *testing.T) {
	r := New()
	r.Report(Issue{Message: "oops"})
	if r.Count() != 0 {
		t.Errorf("issue with empty ID should have been dropped")
	}
}

// TestRegistry_ListOrdering pins the ordering contract: errors before
// warnings, then by scope/camera/id alphabetically. Stable ordering
// keeps the metrics page from shuffling each render.
func TestRegistry_ListOrdering(t *testing.T) {
	r := New()
	r.Report(Issue{ID: "z", Severity: SeverityWarn, Scope: "config"})
	r.Report(Issue{ID: "a", Severity: SeverityError, Scope: "record"})
	r.Report(Issue{ID: "m", Severity: SeverityError, Scope: "config"})

	list := r.List()
	want := []string{"m", "a", "z"} // error/config/m, error/record/a, warn/config/z
	for i, id := range want {
		if list[i].ID != id {
			t.Errorf("list[%d].ID = %q, want %q (full order: %+v)", i, list[i].ID, id, listIDs(list))
		}
	}
}

func listIDs(xs []Issue) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}
