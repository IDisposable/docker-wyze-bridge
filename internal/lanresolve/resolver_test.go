package lanresolve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeProber is a scripted LAN: at maps IP -> the MAC that answers
// there. It records every probe and sweep so tests can assert on the
// ladder's order and on what was not done.
type fakeProber struct {
	mu       sync.Mutex
	at       map[string]string
	probes   []string
	wants    []string // the MAC each probe was told to expect
	sweeps   [][]net.IP
	probeErr map[string]error
	sweepErr error
	delay    time.Duration
	onSweep  func() // called inside sweep, e.g. to cancel the context mid-way
}

func newFakeProber(at map[string]string) *fakeProber {
	return &fakeProber{at: at, probeErr: map[string]error{}}
}

func (f *fakeProber) name() string { return "fake" }

func (f *fakeProber) probe(ctx context.Context, ip net.IP, want string) (string, error) {
	f.mu.Lock()
	f.probes = append(f.probes, ip.String())
	f.wants = append(f.wants, want)
	err := f.probeErr[ip.String()]
	mac := f.at[ip.String()]
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", nil
		}
	}
	if err != nil {
		return "", err
	}
	return mac, nil
}

// sweep behaves like the unicast ARP engine: the requests are addressed
// to mac, so only that station can answer and heard holds at most it.
func (f *fakeProber) sweep(_ context.Context, ips []net.IP, mac string) (net.IP, map[string]net.IP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps = append(f.sweeps, ips)
	if f.onSweep != nil {
		f.onSweep()
	}
	if f.sweepErr != nil {
		return nil, nil, f.sweepErr
	}
	heard := make(map[string]net.IP)
	for _, ip := range ips {
		if m, ok := f.at[ip.String()]; ok && m == mac {
			heard[m] = ip
			return ip, heard, nil
		}
	}
	return nil, heard, nil
}

func (f *fakeProber) probed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.probes...)
}

func (f *fakeProber) sweepCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sweeps)
}

// fakeClock lets tests move time for the throttles without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testResolver wires a resolver to a fake prober on a 10.0.0.0/24 LAN
// with an empty neighbor cache unless the test replaces it.
func testResolver(t *testing.T, p *fakeProber, sweep bool) (*Resolver, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	r := newResolver(Config{Sweep: sweep, SweepMax: 254}, testLogger())
	r.engine = p
	r.ifaces = fakeIfaces(fakeIface(t, 1, "eth0", "10.0.0.2/24"))
	r.cache = func() ([]neighbor, error) { return nil, nil }
	r.now = clock.now
	// Waiting for the LAN-wide gap moves the fake clock instead of
	// sleeping, so tests that queue cameras behind a sweep stay fast.
	r.sleep = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		clock.advance(d)
		return true
	}
	return r, clock
}

const (
	camMAC = "D03F2700CA04" // cloud spelling; resolver normalizes
	camNrm = "d03f2700ca04"
)

func target(cloud, last string) Target {
	return Target{Name: "side_garage", MAC: camMAC, CloudIP: cloud, LastIP: last}
}

func TestResolve_CloudIPVerified_OneProbe(t *testing.T) {
	// The healthy case: the camera answers at its cloud IP, so a resolve
	// costs one probe, aimed at the camera's MAC, and no sweep.
	p := newFakeProber(map[string]string{"10.0.0.123": camNrm})
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !res.Verified || res.IP != "10.0.0.123" || res.Source != "cloud" {
		t.Fatalf("res = %+v", res)
	}
	if got := p.probed(); len(got) != 1 {
		t.Errorf("probes = %v, want exactly one", got)
	}
	if p.sweepCount() != 0 {
		t.Error("no sweep should run when the cloud IP verifies")
	}
	p.mu.Lock()
	wants := append([]string(nil), p.wants...)
	p.mu.Unlock()
	if len(wants) != 1 || wants[0] != camNrm {
		t.Errorf("the probe should be told which MAC to expect (unicast), got %v", wants)
	}
}

func TestResolve_LastIPVerified_AfterCloudMissesOrConflicts(t *testing.T) {
	// Cloud IP now belongs to a phone; last-good IP still has the camera.
	p := newFakeProber(map[string]string{"10.0.0.123": "phonephone00", "10.0.0.151": camNrm})
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), target("10.0.0.123", "10.0.0.151"))
	if !res.Verified || res.IP != "10.0.0.151" || res.Source != "last" {
		t.Fatalf("res = %+v", res)
	}
	if got := p.probed(); len(got) != 2 || got[0] != "10.0.0.123" || got[1] != "10.0.0.151" {
		t.Errorf("probe order = %v, want cloud then last", got)
	}
}

func TestResolve_LastEqualsCloud_ProbedOnce(t *testing.T) {
	// When the last good IP is the same as the cloud IP it is probed
	// once, not twice, before the camera is left unverified.
	p := newFakeProber(map[string]string{})
	r, _ := testResolver(t, p, false)
	res := r.Resolve(context.Background(), target("10.0.0.123", "10.0.0.123"))
	if res.Verified || res.IP != "10.0.0.123" || res.Source != "unverified" {
		t.Fatalf("res = %+v", res)
	}
	if got := p.probed(); len(got) != 1 {
		t.Errorf("probes = %v, want one (duplicate candidate skipped)", got)
	}
}

func TestResolve_NeighCacheHint_FreshestFirst_NoReprobe(t *testing.T) {
	// Kernel cache entries for the MAC are tried live first, one already
	// probed as the cloud IP isn't asked again, and a hit there means
	// no sweep.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm})
	r, _ := testResolver(t, p, true)
	r.cache = func() ([]neighbor, error) {
		return []neighbor{
			{IP: net.ParseIP("10.0.0.123").To4(), MAC: camNrm, State: nudStale, Age: 5 * time.Second},      // the stale cloud IP
			{IP: net.ParseIP("10.0.0.140").To4(), MAC: camNrm, State: nudStale, Age: 2 * time.Second},      // older address, nobody there now
			{IP: net.ParseIP("10.0.0.151").To4(), MAC: camNrm, State: nudReachable, Age: 30 * time.Second}, // where it is
		}, nil
	}
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !res.Verified || res.IP != "10.0.0.151" || res.Source != "neigh" {
		t.Fatalf("res = %+v", res)
	}
	// cloud, then the live cache entry; the stale duplicate of the cloud
	// IP must not be probed twice and .140 is never reached.
	if got := p.probed(); len(got) != 2 || got[0] != "10.0.0.123" || got[1] != "10.0.0.151" {
		t.Errorf("probes = %v", got)
	}
	if p.sweepCount() != 0 {
		t.Error("cache hit should prevent a sweep")
	}
}

func TestResolve_NeighCacheHintWrong_FallsThroughToSweep(t *testing.T) {
	// A cache hint that doesn't answer falls through to the sweep, and
	// the sweep skips every address already probed and our own.
	p := newFakeProber(map[string]string{"10.0.0.77": camNrm})
	r, _ := testResolver(t, p, true)
	r.cache = func() ([]neighbor, error) {
		return []neighbor{{IP: net.ParseIP("10.0.0.140").To4(), MAC: camNrm, State: nudStale}}, nil
	}
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !res.Verified || res.IP != "10.0.0.77" || res.Source != "sweep" {
		t.Fatalf("res = %+v", res)
	}
	// cloud, the cache hint, then the seeding probe of the swept address
	if got := p.probed(); len(got) != 3 || got[1] != "10.0.0.140" || got[2] != "10.0.0.77" {
		t.Errorf("probes = %v, want cloud, the cache hint, then the sweep's find", got)
	}
	// Addresses already probed are left out of the sweep.
	for _, ip := range p.sweeps[0] {
		if s := ip.String(); s == "10.0.0.123" || s == "10.0.0.140" || s == "10.0.0.2" {
			t.Errorf("sweep re-asked %s", s)
		}
	}
}

func TestResolve_NeighCacheError_Ignored(t *testing.T) {
	// A neighbor cache that can't be read (no netlink permission, say) is
	// just skipped, and the sweep still finds the camera.
	p := newFakeProber(map[string]string{"10.0.0.77": camNrm})
	r, _ := testResolver(t, p, true)
	r.cache = func() ([]neighbor, error) { return nil, errors.New("netlink: EPERM") }
	if res := r.Resolve(context.Background(), target("10.0.0.123", "")); !res.Verified || res.Source != "sweep" {
		t.Fatalf("res = %+v", res)
	}
}

func TestResolve_SweepIsPerCamera(t *testing.T) {
	// Sweeps are addressed to one camera's MAC, so a second camera that
	// moved needs a sweep of its own, after the LAN-wide gap; the first
	// camera's result is remembered for the first camera only.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm, "10.0.0.184": "d03f2700ca03", "10.0.0.1": "router000000"})
	r, clock := testResolver(t, p, true)

	first := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !first.Verified || first.IP != "10.0.0.151" || first.Source != "sweep" {
		t.Fatalf("first = %+v", first)
	}
	// The second camera waits out the LAN-wide gap and then gets a
	// sweep of its own (the fake sleep moves the clock).
	b := Target{Name: "driveway", MAC: "D03F2700CA03", CloudIP: "10.0.0.99"}
	before := clock.now()
	second := r.Resolve(context.Background(), b)
	if !second.Verified || second.IP != "10.0.0.184" || second.Source != "sweep" || p.sweepCount() != 2 {
		t.Fatalf("second = %+v sweeps=%d", second, p.sweepCount())
	}
	if waited := clock.now().Sub(before); waited < sweepGlobalGap {
		t.Errorf("second camera should have waited for the gap, waited %v", waited)
	}
	// Resolved again right away: the remembered address answers, no sweep.
	again := r.Resolve(context.Background(), Target{Name: "driveway", MAC: "D03F2700CA03", CloudIP: "10.0.0.99"})
	if !again.Verified || again.Source != "recent" || p.sweepCount() != 2 {
		t.Errorf("repeat = %+v sweeps=%d", again, p.sweepCount())
	}
}

func TestResolve_RecentThatStoppedVerifyingIsForgotten(t *testing.T) {
	// The camera moved twice inside seenTTL: the remembered address no
	// longer answers, so it is dropped and the sweep finds the new one.
	p := newFakeProber(map[string]string{"10.0.0.177": camNrm})
	r, _ := testResolver(t, p, true)
	r.remember(map[string]net.IP{camNrm: net.ParseIP("10.0.0.151").To4()})
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !res.Verified || res.IP != "10.0.0.177" || res.Source != "sweep" {
		t.Fatalf("res = %+v", res)
	}
	if got := p.probed(); len(got) != 3 || got[1] != "10.0.0.151" || got[2] != "10.0.0.177" {
		t.Errorf("probes = %v, want cloud, the stale observation, then the sweep's find", got)
	}
	if got := r.recent(camNrm); got == nil || got.String() != "10.0.0.177" {
		t.Errorf("observation should now point at the new address, got %v", got)
	}
}

func TestResolve_SweepErrorRecordsNothing(t *testing.T) {
	// A sweep that errors out proved nothing, so it must not start a
	// backoff for the camera or count as the LAN's last sweep.
	p := newFakeProber(map[string]string{})
	p.sweepErr = errors.New("socket closed")
	r, _ := testResolver(t, p, true)
	r.Resolve(context.Background(), target("10.0.0.123", ""))
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lastSweep.IsZero() || len(r.camNext) != 0 {
		t.Error("a failed sweep must not schedule backoff or count as a sweep")
	}
}

func TestResolve_RecentExpires(t *testing.T) {
	// An address a sweep heard more than seenTTL ago is too old to lean
	// on, so it isn't used and the camera gets a fresh sweep instead.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm})
	r, clock := testResolver(t, p, true)
	r.remember(map[string]net.IP{camNrm: net.ParseIP("10.0.0.151").To4()})
	clock.advance(seenTTL + time.Second)
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if res.Source != "sweep" {
		t.Errorf("expired observation should not be used; source = %s", res.Source)
	}
}

func TestResolve_SweepDisabled(t *testing.T) {
	// With Sweep off, a camera that moved stays unverified at its cloud
	// IP and no sweep runs, even though one would have found it.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm})
	r, _ := testResolver(t, p, false)
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if res.Verified || res.IP != "10.0.0.123" || res.Source != "unverified" {
		t.Fatalf("res = %+v", res)
	}
	if p.sweepCount() != 0 {
		t.Error("sweep ran with Sweep=false")
	}
}

func TestResolve_SweepThrottles(t *testing.T) {
	// Sweeps are paced: another camera waits out the LAN-wide gap, a
	// camera that missed waits its own backoff, which doubles on the next
	// miss, and a resolve cancelled while waiting doesn't sweep at all.
	p := newFakeProber(map[string]string{}) // nobody answers: every sweep misses
	r, clock := testResolver(t, p, true)
	r.cfg.SweepMax = 100
	a := target("10.0.0.123", "")
	b := Target{Name: "driveway", MAC: "D03F2700CA03", CloudIP: "10.0.0.184"}

	r.Resolve(context.Background(), a)
	if p.sweepCount() != 1 {
		t.Fatalf("first resolve should sweep, got %d", p.sweepCount())
	}
	// Another camera right away waits out the LAN-wide gap, then sweeps.
	start := clock.now()
	r.Resolve(context.Background(), b)
	if p.sweepCount() != 2 || clock.now().Sub(start) < sweepGlobalGap {
		t.Errorf("second camera: sweeps=%d waited=%v", p.sweepCount(), clock.now().Sub(start))
	}
	// Camera a again: its own backoff (1m after a miss) is not waited
	// out, so no sweep.
	clock.advance(sweepGlobalGap)
	r.Resolve(context.Background(), a)
	if p.sweepCount() != 2 {
		t.Errorf("per-camera backoff (%v) not honored", sweepCamGap)
	}
	clock.advance(sweepCamGap)
	r.Resolve(context.Background(), a)
	if p.sweepCount() != 3 {
		t.Errorf("camera a should sweep after its first backoff, got %d", p.sweepCount())
	}
	// Second consecutive miss doubles the gap: 2m.
	clock.advance(sweepCamGap + sweepGlobalGap)
	r.Resolve(context.Background(), a)
	if p.sweepCount() != 3 {
		t.Errorf("backoff should have doubled to %v", 2*sweepCamGap)
	}
	clock.advance(sweepCamGap)
	r.Resolve(context.Background(), a)
	if p.sweepCount() != 4 {
		t.Errorf("camera a should sweep after the doubled gap, got %d", p.sweepCount())
	}
	// A resolve whose context ends while it waits for the gap comes
	// back unverified and uncounted.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := r.Resolve(ctx, b); res.Verified || p.sweepCount() != 4 {
		t.Errorf("cancelled wait: %+v sweeps=%d", res, p.sweepCount())
	}
}

func TestResolve_RepeatInsideBackoffDoesNotSweepAgain(t *testing.T) {
	// A camera the sweep couldn't find is not swept for again on the
	// very next resolve; it waits out its backoff, and the throttled
	// resolve doesn't inflate its miss count.
	p := newFakeProber(map[string]string{"10.0.0.1": "aabbccddeeff"})
	r, clock := testResolver(t, p, true)
	a := target("10.0.0.123", "")
	if res := r.Resolve(context.Background(), a); res.Verified || p.sweepCount() != 1 || res.Misses != 1 {
		t.Fatalf("first: %+v sweeps=%d", res, p.sweepCount())
	}
	clock.advance(sweepGlobalGap + time.Second)
	if res := r.Resolve(context.Background(), a); res.Verified || p.sweepCount() != 1 || res.Misses != 1 {
		t.Errorf("inside backoff: %+v sweeps=%d", res, p.sweepCount())
	}
	clock.advance(sweepCamGap)
	if res := r.Resolve(context.Background(), a); p.sweepCount() != 2 || res.Misses != 2 {
		t.Errorf("after backoff: %+v sweeps=%d", res, p.sweepCount())
	}
}

func TestResolve_CancelledSweepIsNotCounted(t *testing.T) {
	// The context dies while the sweep is running (the bridge is
	// shutting down, or the per-resolve budget ran out). A sweep cut
	// short proved nothing, so it must leave no backoff for the camera
	// and must not count as having covered the LAN.
	p := newFakeProber(map[string]string{})
	r, _ := testResolver(t, p, true)
	ctx, cancel := context.WithCancel(context.Background())
	p.onSweep = cancel
	res := r.Resolve(ctx, target("10.0.0.123", ""))
	if res.Verified || p.sweepCount() != 1 {
		t.Fatalf("res=%+v sweeps=%d", res, p.sweepCount())
	}
	r.mu.Lock()
	_, backoff := r.camNext[camNrm]
	last := !r.lastSweep.IsZero()
	misses := r.camMiss[camNrm]
	r.mu.Unlock()
	if backoff || last || misses != 0 {
		t.Errorf("cancelled sweep recorded backoff=%v lastSweep=%v misses=%d", backoff, last, misses)
	}
}

func TestResolve_SweepBackoffCapsAndResets(t *testing.T) {
	// However many misses pile up, the per-camera backoff stops growing
	// at sweepCamGapMax, and a single hit clears both the backoff and
	// the miss count.
	p := newFakeProber(map[string]string{})
	r, clock := testResolver(t, p, true)
	for i := 0; i < 12; i++ {
		r.noteSweep(camNrm, false)
		clock.advance(time.Millisecond)
	}
	next := r.camNext[camNrm]
	if gap := next.Sub(clock.now()); gap > sweepCamGapMax || gap < sweepCamGapMax-time.Second {
		t.Errorf("gap after 12 misses = %v, want capped at %v", gap, sweepCamGapMax)
	}
	r.noteSweep(camNrm, true)
	if _, ok := r.camNext[camNrm]; ok {
		t.Error("a hit should clear the backoff")
	}
	if r.camMiss[camNrm] != 0 {
		t.Error("a hit should clear the miss count")
	}
}

func TestResolve_SweepError_Unverified(t *testing.T) {
	// When the sweep fails outright, the caller still gets the cloud IP
	// it started with, marked unverified.
	p := newFakeProber(map[string]string{})
	p.sweepErr = errors.New("socket closed")
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if res.Verified || res.IP != "10.0.0.123" {
		t.Fatalf("res = %+v", res)
	}
}

func TestResolve_NoMACOrNoEngine_SkipsEverything(t *testing.T) {
	// With no MAC to match or no engine to ask, nothing is probed and the
	// cloud IP comes back unverified; the engine reports as "none".
	p := newFakeProber(map[string]string{"10.0.0.123": camNrm})
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), Target{Name: "x", MAC: "", CloudIP: "10.0.0.123"})
	if res.Verified || res.IP != "10.0.0.123" || len(p.probed()) != 0 {
		t.Errorf("no MAC: res=%+v probes=%v", res, p.probed())
	}
	r.engine = nil
	res = r.Resolve(context.Background(), target("10.0.0.123", ""))
	if res.Verified || res.IP != "10.0.0.123" || r.Engine() != "none" {
		t.Errorf("no engine: res=%+v engine=%s", res, r.Engine())
	}
}

func TestResolve_EmptyCloudIP_StillSearches(t *testing.T) {
	// A camera the cloud gave no IP for can still be found: with nothing
	// to probe first, the ladder goes straight to the sweep.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm})
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), target("", ""))
	if !res.Verified || res.IP != "10.0.0.151" || res.Source != "sweep" {
		t.Fatalf("res = %+v", res)
	}
	// No cloud or last address to probe before the sweep; the only
	// probe is the one that seeds the kernel with the sweep's find.
	if got := p.probed(); len(got) != 1 || got[0] != "10.0.0.151" {
		t.Errorf("probes = %v, want only the post-sweep seed", got)
	}
}

func TestResolve_GarbageIPs_Ignored(t *testing.T) {
	// Cloud and last IPs that don't parse are skipped instead of breaking
	// the resolve, and the sweep still finds the camera.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm})
	r, _ := testResolver(t, p, true)
	res := r.Resolve(context.Background(), target("not-an-ip", "also-bad"))
	if !res.Verified || res.IP != "10.0.0.151" {
		t.Fatalf("res = %+v", res)
	}
}

func TestResolve_CancelledContext_ReturnsFast(t *testing.T) {
	// An already cancelled context makes Resolve hand back the cloud IP
	// unverified right away, without waiting on slow probes or sweeping.
	p := newFakeProber(map[string]string{})
	p.delay = 50 * time.Millisecond
	r, _ := testResolver(t, p, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	res := r.Resolve(ctx, target("10.0.0.123", "10.0.0.151"))
	if res.Verified || res.IP != "10.0.0.123" {
		t.Errorf("res = %+v", res)
	}
	if time.Since(start) > 100*time.Millisecond || p.sweepCount() != 0 {
		t.Errorf("cancelled resolve took %v with %d sweeps", time.Since(start), p.sweepCount())
	}
}

func TestResolve_Elapsed(t *testing.T) {
	// Elapsed is taken from the resolver's clock and covers the time the
	// probes took (here a single 7ms probe).
	p := newFakeProber(map[string]string{"10.0.0.123": camNrm})
	r, clock := testResolver(t, p, true)
	r.engine = &tickingProber{fakeProber: p, clock: clock, step: 7 * time.Millisecond}
	res := r.Resolve(context.Background(), target("10.0.0.123", ""))
	if res.Elapsed != 7*time.Millisecond {
		t.Errorf("Elapsed = %v, want 7ms", res.Elapsed)
	}
}

// tickingProber advances the fake clock on every probe so Elapsed is
// observable without real time passing.
type tickingProber struct {
	*fakeProber
	clock *fakeClock
	step  time.Duration
}

func (p *tickingProber) probe(ctx context.Context, ip net.IP, want string) (string, error) {
	p.clock.advance(p.step)
	return p.fakeProber.probe(ctx, ip, want)
}

func TestResolve_Concurrent_OneSweepAtATime(t *testing.T) {
	// 20 cameras all moved at once. Sweeps are per camera and
	// serialized LAN-wide with a gap between them, so the resolves
	// queue up and each gets its own sweep in turn (the fake sleep
	// moves the clock through the gaps). With the race detector on,
	// this also checks the locking.
	at := map[string]string{}
	var targets []Target
	for i := 0; i < 20; i++ {
		mac := fmt.Sprintf("d03f27a8%04x", i)
		at[fmt.Sprintf("10.0.0.%d", 100+i)] = mac
		targets = append(targets, Target{Name: fmt.Sprintf("cam%d", i), MAC: mac, CloudIP: fmt.Sprintf("10.0.0.%d", 30+i)})
	}
	p := newFakeProber(at)
	r, _ := testResolver(t, p, true)
	r.cfg.SweepMax = 254

	var wg sync.WaitGroup
	results := make([]Result, len(targets))
	for i, tg := range targets {
		wg.Add(1)
		go func(i int, tg Target) {
			defer wg.Done()
			results[i] = r.Resolve(context.Background(), tg)
		}(i, tg)
	}
	wg.Wait()
	for i, res := range results {
		if want := fmt.Sprintf("10.0.0.%d", 100+i); !res.Verified || res.IP != want || res.Source != "sweep" {
			t.Errorf("cam%d: %+v, want %s via sweep", i, res, want)
		}
	}
	if p.sweepCount() != 20 {
		t.Errorf("sweeps = %d, want one per camera", p.sweepCount())
	}
}

func TestCheck(t *testing.T) {
	// Check says yes only when the expected MAC holds the address, no for
	// another device or silence, and returns an error for a bad IP or
	// MAC, a failed probe, or no engine.
	p := newFakeProber(map[string]string{"10.0.0.151": camNrm, "10.0.0.123": "phonephone00"})
	r, _ := testResolver(t, p, true)
	ctx := context.Background()

	if ok, err := r.Check(ctx, "10.0.0.151", camMAC); err != nil || !ok {
		t.Errorf("match: %v, %v", ok, err)
	}
	if ok, err := r.Check(ctx, "10.0.0.123", camMAC); err != nil || ok {
		t.Errorf("other device: %v, %v", ok, err)
	}
	if ok, err := r.Check(ctx, "10.0.0.9", camMAC); err != nil || ok {
		t.Errorf("nobody: %v, %v", ok, err)
	}
	if _, err := r.Check(ctx, "bad", camMAC); !errors.Is(err, errBadTarget) {
		t.Errorf("bad ip: %v", err)
	}
	if _, err := r.Check(ctx, "10.0.0.151", "zz"); !errors.Is(err, errBadTarget) {
		t.Errorf("bad mac: %v", err)
	}
	boom := errors.New("down")
	p.probeErr["10.0.0.151"] = boom
	if _, err := r.Check(ctx, "10.0.0.151", camMAC); !errors.Is(err, boom) {
		t.Errorf("probe error not propagated: %v", err)
	}
	r.engine = nil
	if _, err := r.Check(ctx, "10.0.0.151", camMAC); !errors.Is(err, errNoEngine) {
		t.Errorf("no engine: %v", err)
	}
}

func TestNew_NeverNil(t *testing.T) {
	// Whatever the host allows, New returns a usable Resolver.
	r := New(Config{Sweep: true}, testLogger())
	if r == nil {
		t.Fatal("New returned nil")
	}
	switch r.Engine() {
	case "arp", "neigh", "none":
	default:
		t.Errorf("unexpected engine %q", r.Engine())
	}
	if r.cfg.SweepMax != defaultMax {
		t.Errorf("SweepMax default = %d", r.cfg.SweepMax)
	}
}

func TestResolve_MissesCountOwnSweepsOnly(t *testing.T) {
	// a is not on this LAN. Each of its own sweeps that doesn't hear it
	// counts once; a throttled resolve doesn't, another camera's sweep
	// doesn't, and verifying resets everything.
	p := newFakeProber(map[string]string{"10.0.0.1": "aabbccddeeff"})
	r, clock := testResolver(t, p, true)
	a := target("10.0.0.123", "")
	b := Target{Name: "driveway", MAC: "D03F2700CA03", CloudIP: "10.0.0.184"}

	if res := r.Resolve(context.Background(), a); res.Verified || res.Misses != 1 {
		t.Fatalf("after own sweep: %+v", res)
	}
	if res := r.Resolve(context.Background(), a); res.Misses != 1 || p.sweepCount() != 1 {
		t.Errorf("throttled resolve counted: %+v sweeps=%d", res, p.sweepCount())
	}
	clock.advance(sweepGlobalGap + time.Second)
	if res := r.Resolve(context.Background(), b); res.Misses != 1 || p.sweepCount() != 2 {
		t.Fatalf("b: %+v sweeps=%d", res, p.sweepCount())
	}
	clock.advance(sweepGlobalGap + time.Second)
	if res := r.Resolve(context.Background(), a); res.Misses != 1 {
		t.Errorf("b's sweep must not count against a: %+v", res)
	}
	clock.advance(sweepCamGap)
	if res := r.Resolve(context.Background(), a); res.Misses != 2 || p.sweepCount() != 3 {
		t.Errorf("a's second own sweep: %+v sweeps=%d", res, p.sweepCount())
	}
	// A camera that verifies, by any rung, starts from zero and may
	// sweep again right away the next time it goes missing.
	p.mu.Lock()
	p.at["10.0.0.123"] = camNrm
	p.mu.Unlock()
	if res := r.Resolve(context.Background(), a); !res.Verified || res.Misses != 0 {
		t.Fatalf("verified: %+v", res)
	}
	r.mu.Lock()
	_, backoff := r.camNext[camNrm]
	miss := r.camMiss[camNrm]
	r.mu.Unlock()
	if backoff || miss != 0 {
		t.Errorf("verify should clear the backoff and misses: backoff=%v misses=%d", backoff, miss)
	}
	// Never-seen camera with nothing swept yet: zero, not an error.
	r2, _ := testResolver(t, newFakeProber(map[string]string{}), false)
	if res := r2.Resolve(context.Background(), a); res.Misses != 0 {
		t.Errorf("no sweeps at all: %+v", res)
	}
}

func TestResolve_NoSweepStopsAtTheCheapRungs(t *testing.T) {
	// A camera that was heard from a few minutes ago and has gone quiet
	// is dozing: probe its addresses and read the caches, but don't fire
	// a sweep's worth of ARP at it.
	p := newFakeProber(map[string]string{"10.0.0.140": camNrm})
	r, _ := testResolver(t, p, true)
	tg := target("10.0.0.123", "")
	tg.NoSweep = true
	res := r.Resolve(context.Background(), tg)
	if res.Verified || res.IP != "10.0.0.123" {
		t.Errorf("res = %+v, want unverified at the cloud IP", res)
	}
	if p.sweepCount() != 0 {
		t.Errorf("swept %d times with NoSweep", p.sweepCount())
	}
	if len(p.probes) == 0 {
		t.Error("the cloud address should still be probed")
	}

	// Same camera without the flag sweeps and finds it.
	res = r.Resolve(context.Background(), target("10.0.0.123", ""))
	if !res.Verified || res.IP != "10.0.0.140" || p.sweepCount() != 1 {
		t.Errorf("without NoSweep: %+v sweeps=%d", res, p.sweepCount())
	}
}
