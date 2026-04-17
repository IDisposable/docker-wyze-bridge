package webui

import (
	"testing"
	"time"
)

func TestEventLog_RecordAndSnapshot(t *testing.T) {
	l := NewEventLog(5)
	base := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		l.Record(Event{
			Time:    base.Add(time.Duration(i) * time.Second),
			Kind:    "state",
			Camera:  "front_door",
			Message: "transition",
		})
	}
	snap := l.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len = %d", len(snap))
	}
	// Newest first
	if !snap[0].Time.Equal(base.Add(2 * time.Second)) {
		t.Errorf("snap[0] = %v, want newest (base+2s)", snap[0].Time)
	}
	if !snap[2].Time.Equal(base) {
		t.Errorf("snap[2] = %v, want oldest (base)", snap[2].Time)
	}
}

func TestEventLog_RingBufferEviction(t *testing.T) {
	l := NewEventLog(3)
	for i := 0; i < 5; i++ {
		l.Record(Event{Kind: "state", Camera: "c", Message: "m", Time: time.Unix(int64(i), 0)})
	}
	if l.Len() != 3 {
		t.Fatalf("len = %d, want 3 (capped)", l.Len())
	}
	snap := l.Snapshot()
	// After 5 records into cap=3, we should have the LAST 3 events.
	// Newest first: t=4, t=3, t=2.
	wantTimes := []int64{4, 3, 2}
	for i, want := range wantTimes {
		if snap[i].Time.Unix() != want {
			t.Errorf("snap[%d].Time = %d, want %d", i, snap[i].Time.Unix(), want)
		}
	}
}

func TestEventLog_AutoTimestamp(t *testing.T) {
	l := NewEventLog(3)
	before := time.Now()
	l.Record(Event{Kind: "state", Camera: "c", Message: "m"})
	after := time.Now()

	snap := l.Snapshot()
	if snap[0].Time.Before(before) || snap[0].Time.After(after) {
		t.Errorf("auto-stamped time %v not in [%v, %v]", snap[0].Time, before, after)
	}
}
