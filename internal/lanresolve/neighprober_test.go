package lanresolve

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeCache scripts the neighbor table: each read returns the next
// snapshot in the sequence (the last one repeats), and records every
// nudge so tests can assert on ordering and counts.
type fakeCache struct {
	mu        sync.Mutex
	snapshots [][]neighbor
	reads     int
	nudged    []string
	readErr   error
	nudgeErr  error
}

func (f *fakeCache) read() ([]neighbor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	i := min(f.reads, len(f.snapshots)-1)
	f.reads++
	if i < 0 {
		return nil, nil
	}
	return f.snapshots[i], nil
}

func (f *fakeCache) nudge(_ context.Context, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nudged = append(f.nudged, ip.String())
	return f.nudgeErr
}

func (f *fakeCache) nudges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.nudged...)
}

func newFakeNeighEngine(t *testing.T, fc *fakeCache) *neighEngine {
	t.Helper()
	return &neighEngine{
		ifaces:     fakeIfaces(fakeIface(t, 1, "eth0", "10.0.0.2/24")),
		read:       fc.read,
		nudge:      fc.nudge,
		poll:       5 * time.Millisecond,
		chunk:      2,
		chunkPause: 5 * time.Millisecond,
	}
}

func entry(ip string, mac string, state uint16) neighbor {
	return neighbor{IP: net.ParseIP(ip).To4(), MAC: mac, State: state}
}

const cam = "d03f2700ca04"

func TestNeighEngine_ProbeCachedLive_NoNudge(t *testing.T) {
	// An address the cache already has live is answered straight from
	// the cache, with no UDP nudge sent.
	fc := &fakeCache{snapshots: [][]neighbor{{entry("10.0.0.5", cam, nudReachable)}}}
	eng := newFakeNeighEngine(t, fc)
	got, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), "")
	if err != nil || got != cam {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if len(fc.nudges()) != 0 {
		t.Errorf("a cached live entry should not be nudged: %v", fc.nudges())
	}
}

func TestNeighEngine_ProbeCachedStale_TrustedWithoutNudge(t *testing.T) {
	// Documented weakness of this engine: STALE is taken at face value.
	fc := &fakeCache{snapshots: [][]neighbor{{entry("10.0.0.5", cam, nudStale)}}}
	eng := newFakeNeighEngine(t, fc)
	got, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), "")
	if err != nil || got != cam {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if len(fc.nudges()) != 0 {
		t.Errorf("unexpected nudges: %v", fc.nudges())
	}
}

func TestNeighEngine_ProbeAbsent_NudgesThenPolls(t *testing.T) {
	// With nothing cached, the engine nudges the address once and keeps
	// reading the cache until the kernel's ARP finishes.
	fc := &fakeCache{snapshots: [][]neighbor{
		nil,                                    // first read: nothing cached
		{entry("10.0.0.5", "", nudIncomplete)}, // kernel ARPing
		{entry("10.0.0.5", cam, nudReachable)}, // answered
	}}
	eng := newFakeNeighEngine(t, fc)
	got, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), "")
	if err != nil || got != cam {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if n := fc.nudges(); len(n) != 1 || n[0] != "10.0.0.5" {
		t.Errorf("nudges = %v, want exactly [10.0.0.5]", n)
	}
}

func TestNeighEngine_ProbeAbsent_FailedEndsEarly(t *testing.T) {
	// Once the kernel marks the entry FAILED the probe stops with no
	// answer, instead of waiting out the rest of probeTimeout.
	fc := &fakeCache{snapshots: [][]neighbor{nil, {entry("10.0.0.5", "", nudFailed)}}}
	eng := newFakeNeighEngine(t, fc)
	start := time.Now()
	got, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), "")
	if err != nil || got != "" {
		t.Fatalf("probe = %q, %v; want \"\", nil", got, err)
	}
	if time.Since(start) > probeTimeout/2 {
		t.Errorf("FAILED entry should end the wait early, took %v", time.Since(start))
	}
}

func TestNeighEngine_ProbeAbsent_TimesOut(t *testing.T) {
	// If the cache never gets an entry, the probe reads as "nobody home"
	// (no error) after about probeTimeout.
	fc := &fakeCache{snapshots: [][]neighbor{nil}}
	eng := newFakeNeighEngine(t, fc)
	start := time.Now()
	got, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), "")
	if err != nil || got != "" {
		t.Fatalf("probe = %q, %v; want \"\", nil", got, err)
	}
	if took := time.Since(start); took < probeTimeout-50*time.Millisecond || took > probeTimeout+300*time.Millisecond {
		t.Errorf("took %v, want ≈%v", took, probeTimeout)
	}
}

func TestNeighEngine_ProbeAbsent_ContextCancel(t *testing.T) {
	// A context that ends while the probe is polling stops it promptly,
	// with no answer and no error.
	fc := &fakeCache{snapshots: [][]neighbor{nil}}
	eng := newFakeNeighEngine(t, fc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got, err := eng.probe(ctx, net.ParseIP("10.0.0.5"), ""); err != nil || got != "" {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Error("cancelled context should end the probe promptly")
	}
}

func TestNeighEngine_ProbeErrors(t *testing.T) {
	// An off-link address, a failed cache read and a failed nudge all come
	// back as errors, since in each case the question was never asked.
	eng := newFakeNeighEngine(t, &fakeCache{})
	if _, err := eng.probe(context.Background(), net.ParseIP("192.0.2.1"), ""); !errors.Is(err, errOffLink) {
		t.Errorf("off-link: err = %v", err)
	}
	boom := errors.New("netlink down")
	eng = newFakeNeighEngine(t, &fakeCache{readErr: boom})
	if _, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), ""); !errors.Is(err, boom) {
		t.Errorf("read error not propagated: %v", err)
	}
	eng = newFakeNeighEngine(t, &fakeCache{snapshots: [][]neighbor{nil}, nudgeErr: boom})
	if _, err := eng.probe(context.Background(), net.ParseIP("10.0.0.5"), ""); !errors.Is(err, boom) {
		t.Errorf("nudge error not propagated: %v", err)
	}
}

func TestNeighEngine_SweepFindsInSecondChunk(t *testing.T) {
	// The sweep nudges in chunks and checks the cache after each one,
	// stopping at the chunk that turns up the MAC. Every live entry it
	// saw along the way ends up in heard.
	fc := &fakeCache{snapshots: [][]neighbor{
		{entry("10.0.0.1", "aaaaaaaaaaaa", nudReachable)},                                                                         // after chunk 1
		{entry("10.0.0.1", "aaaaaaaaaaaa", nudReachable), entry("10.0.0.4", cam, nudReachable), entry("10.0.0.9", cam, nudStale)}, // after chunk 2
	}}
	eng := newFakeNeighEngine(t, fc)
	ips := []net.IP{
		net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.3"), // chunk 1
		net.ParseIP("10.0.0.4"), net.ParseIP("10.0.0.6"), // chunk 2
		net.ParseIP("10.0.0.7"), // never reached
	}
	found, heard, err := eng.sweep(context.Background(), ips, cam)
	if err != nil || found == nil || found.String() != "10.0.0.4" {
		t.Fatalf("sweep = %v, %v", found, err)
	}
	if n := fc.nudges(); len(n) != 4 || n[3] != "10.0.0.6" {
		t.Errorf("nudges = %v, want the first two chunks only", n)
	}
	if heard["aaaaaaaaaaaa"].String() != "10.0.0.1" || heard[cam].String() != "10.0.0.4" {
		t.Errorf("heard = %v", heard)
	}
}

func TestNeighEngine_SweepMiss(t *testing.T) {
	// A sweep that never hears the MAC returns nil with no error and
	// still reports what it did hear. A list with nothing on-link gets
	// errOffLink.
	fc := &fakeCache{snapshots: [][]neighbor{{entry("10.0.0.1", "aaaaaaaaaaaa", nudReachable)}}}
	eng := newFakeNeighEngine(t, fc)
	found, heard, err := eng.sweep(context.Background(), []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2")}, cam)
	if err != nil || found != nil {
		t.Fatalf("sweep = %v, %v; want nil, nil", found, err)
	}
	if len(heard) != 1 {
		t.Errorf("heard = %v", heard)
	}
	_, _, err = eng.sweep(context.Background(), []net.IP{net.ParseIP("192.0.2.1")}, cam)
	if !errors.Is(err, errOffLink) {
		t.Errorf("all off-link: err = %v", err)
	}
}

func TestNeighEngine_SweepContextCancel(t *testing.T) {
	// A sweep whose context is already cancelled returns right away with
	// nothing found and no error, instead of sitting through its pauses.
	fc := &fakeCache{snapshots: [][]neighbor{nil}}
	eng := newFakeNeighEngine(t, fc)
	eng.chunkPause = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	found, _, err := eng.sweep(ctx, hosts(fakeIface(t, 1, "eth0", "10.0.0.2/24").Net, 300), cam)
	if err != nil || found != nil || time.Since(start) > 200*time.Millisecond {
		t.Errorf("cancelled sweep: found=%v err=%v took=%v", found, err, time.Since(start))
	}
}
