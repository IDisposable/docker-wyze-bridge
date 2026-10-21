// Package lanresolve finds the LAN address a TUTK camera is actually
// at. The Wyze cloud reports each camera's IP, but only as of the last
// time the camera phoned home; after a DHCP lease change the cloud can
// lag for hours while go2rtc keeps dialing the old address and timing
// out ("wyze: connect failed: discovery timeout"). Since the cloud also
// reports the camera's hardware address, the LAN itself can be asked
// which IP that MAC is answering from right now.
//
// Resolve runs a fixed ladder, cheapest rung first, and stops at the
// first verified answer:
//
//  1. Probe the cloud IP (and the last address that verified, if it
//     differs). One ARP round trip; a healthy camera is done here.
//  2. Try addresses already associated with the MAC: anything an
//     earlier sweep heard from, then the kernel's neighbor cache. Free
//     when something on the host talked to the camera at its new
//     address, or another camera's sweep already saw it.
//  3. Sweep: ARP every host in the camera's /24, then the rest of the
//     local subnet, until the MAC answers. Bounded, paced, throttled,
//     and every reply is remembered for the other cameras.
//  4. Give up and return the cloud IP unverified, which is exactly what
//     the bridge did before this package existed.
//
// Every rung is keyed on the MAC, so a wrong device at the right IP is
// detected rather than trusted.
package lanresolve

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Config is the operator-facing part of the resolver's behavior; the
// rest (timeouts, pacing) is fixed because the right values depend on
// physics, not preference.
type Config struct {
	// Sweep allows rung 3. Off, a camera that moved is only found when
	// the kernel already knows its new address. Default on.
	Sweep bool
	// SweepMax caps the addresses one sweep may ask about. Default 1024
	// (a /22, eero's default LAN size).
	SweepMax int
}

// Throttles on the sweep, the only rung that generates real traffic.
// A camera that is permanently gone (another household's, say) would
// otherwise sweep the LAN on every reconnect attempt, so its gap
// doubles on each miss up to sweepCamGapMax and resets on a hit.
const (
	sweepGlobalGap = 2 * time.Second // between any two sweeps, LAN-wide (unicast, so this is manners, not necessity)
	sweepCamGap    = 1 * time.Minute // first per-camera gap after a miss
	sweepCamGapMax = 1 * time.Hour
	seenTTL        = 2 * time.Minute  // how long a sweep observation stays usable
	resolveTimeout = 12 * time.Second // overall budget per Resolve call
	defaultMax     = 1024
)

var (
	errNoEngine  = errors.New("no LAN resolver engine available")
	errBadTarget = errors.New("invalid IP or MAC")
)

// Target is what the caller knows about one camera.
type Target struct {
	Name    string // normalized camera name, for logs
	MAC     string // as the Wyze cloud reports it; any spelling
	CloudIP string // LAN IP from the Wyze cloud; may be empty
	LastIP  string // last address that verified, if any; may be empty
}

// Result is the resolver's answer. IP is always set when CloudIP was —
// unverified results fall back to it so callers never lose the address
// they started with.
type Result struct {
	IP       string
	Verified bool
	// Source names the rung that verified the address: "cloud", "last",
	// "recent" (an earlier sweep heard it), "neigh" (kernel cache),
	// "sweep" — or "unverified" when none did.
	Source  string
	Elapsed time.Duration
	// Misses counts this camera's sweeps in a row that didn't hear its
	// MAC. 0 whenever the address verified. Two or more is the WebUI's
	// cue that the camera probably isn't on this LAN at all.
	Misses int
}

// Resolver is safe for concurrent use; the manager calls Resolve for
// every camera in parallel at startup.
type Resolver struct {
	cfg    Config
	log    zerolog.Logger
	engine prober

	// Seams for tests. Production wiring is in New.
	ifaces func() ([]lanInterface, error)
	cache  func() ([]neighbor, error)
	now    func() time.Time
	// sleep waits for d or until ctx is done, reporting which; tests
	// replace it with one that moves the fake clock instead.
	sleep func(ctx context.Context, d time.Duration) bool

	mu        sync.Mutex
	lastSweep time.Time
	camNext   map[string]time.Time // earliest next sweep per MAC
	camMiss   map[string]int       // consecutive sweep misses per MAC
	seen      map[string]seenAddr  // MAC -> address a sweep heard from
	sweepMu   sync.Mutex           // one sweep at a time, LAN-wide
}

type seenAddr struct {
	ip net.IP
	at time.Time
}

// New builds a resolver with the best engine the host allows. It never
// fails: with no usable engine it still returns a Resolver whose
// Resolve hands back the cloud IP, so callers don't need a nil path.
func New(cfg Config, log zerolog.Logger) *Resolver {
	r := newResolver(cfg, log)
	eng, err := newProber(r.ifaces)
	if err != nil {
		log.Warn().Err(err).Msg("LAN IP resolver has no usable engine; cloud IPs will be used as-is")
	} else {
		r.engine = eng
		log.Info().Str("engine", eng.name()).Bool("sweep", cfg.Sweep).Msg("LAN IP resolver ready")
	}
	return r
}

// newResolver wires everything except the engine, so tests can plug a
// fake prober in without touching the network.
func newResolver(cfg Config, log zerolog.Logger) *Resolver {
	if cfg.SweepMax <= 0 {
		cfg.SweepMax = defaultMax
	}
	return &Resolver{
		cfg:     cfg,
		log:     log,
		ifaces:  listInterfaces,
		cache:   readNeighbors,
		now:     time.Now,
		sleep:   sleepCtx,
		camNext: make(map[string]time.Time),
		camMiss: make(map[string]int),
		seen:    make(map[string]seenAddr),
	}
}

// Engine names the active engine ("arp", "neigh") or "none".
func (r *Resolver) Engine() string {
	if r.engine == nil {
		return "none"
	}
	return r.engine.name()
}

// Resolve walks the ladder for one camera. It respects ctx and never
// runs longer than resolveTimeout.
func (r *Resolver) Resolve(ctx context.Context, t Target) Result {
	start := r.now()
	res := Result{IP: t.CloudIP, Source: "unverified"}
	mac := NormalizeMAC(t.MAC)
	// finish stamps the elapsed time and the miss count: a verified
	// address wipes the camera's slate, an unverified one reports how
	// many sweeps have now failed to find it.
	finish := func(res Result) Result {
		res.Elapsed = r.now().Sub(start)
		if res.Verified {
			r.clearMisses(mac)
		} else {
			res.Misses = r.misses(mac)
		}
		return res
	}

	log := r.log.With().Str("cam", t.Name).Str("mac", mac).Str("cloud_ip", t.CloudIP).Logger()
	if mac == "" || r.engine == nil {
		log.Debug().Msg("lan resolve skipped: no MAC or no engine")
		return finish(res)
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	// tried keeps every rung from re-probing an address another rung
	// already asked about.
	tried := make(map[string]bool)
	probe := func(ip net.IP, source string) (Result, bool) {
		key := ip.String()
		if tried[key] {
			return Result{}, false
		}
		tried[key] = true
		got, err := r.engine.probe(ctx, ip, mac)
		switch {
		case err != nil:
			log.Debug().Err(err).Str("ip", key).Str("rung", source).Msg("lan resolve: probe failed")
		case got == mac:
			return Result{IP: key, Verified: true, Source: source}, true
		case got != "":
			log.Debug().Str("ip", key).Str("rung", source).Str("holder", got).Msg("lan resolve: address belongs to another device")
		default:
			log.Debug().Str("ip", key).Str("rung", source).Msg("lan resolve: no answer")
		}
		return Result{}, false
	}

	// Rung 1: the addresses we already believe in.
	for _, c := range []struct{ ip, source string }{{t.CloudIP, "cloud"}, {t.LastIP, "last"}} {
		if ip := net.ParseIP(c.ip); ip != nil {
			if hit, ok := probe(ip, c.source); ok {
				return finish(hit)
			}
		}
		if ctx.Err() != nil {
			return finish(res)
		}
	}

	// Rung 2a: an earlier sweep (for any camera) heard this MAC. An
	// observation that no longer verifies is dropped so the camera
	// doesn't keep being probed at an address it has already left.
	if ip := r.recent(mac); ip != nil {
		if hit, ok := probe(ip, "recent"); ok {
			return finish(hit)
		}
		r.forget(mac)
	}

	// Rung 2b: the kernel's neighbor cache, freshest entry first.
	if r.cache != nil && ctx.Err() == nil {
		if entries, err := r.cache(); err == nil {
			for _, n := range findByMAC(entries, mac) {
				if hit, ok := probe(n.IP, "neigh"); ok {
					return finish(hit)
				}
				if ctx.Err() != nil {
					return finish(res)
				}
			}
		}
	}

	// Rung 3: ask every address, addressed to this camera's MAC. The
	// allowed check happens under sweepMu so a second resolve of the
	// same camera queued behind its sweep sees the result (via recent)
	// rather than starting another. A camera that only has to wait for
	// the LAN-wide gap waits (within the resolve budget) instead of
	// giving up, so when several cameras come up missing together they
	// each get their sweep in one pass; one inside its own backoff
	// doesn't.
	if r.cfg.Sweep && ctx.Err() == nil {
		ifaces, err := r.ifaces()
		if err == nil {
			hints := []net.IP{net.ParseIP(t.CloudIP), net.ParseIP(t.LastIP)}
			addrs := without(sweepAddresses(ifaces, hints, r.cfg.SweepMax), tried)
			for len(addrs) > 0 && ctx.Err() == nil {
				r.sweepMu.Lock()
				if ip := r.recent(mac); ip != nil {
					// A sweep that just finished answered our question.
					r.sweepMu.Unlock()
					if hit, ok := probe(ip, "recent"); ok {
						return finish(hit)
					}
					r.forget(mac)
					break
				}
				wait, backoff := r.sweepWait(mac)
				if backoff {
					r.sweepMu.Unlock()
					log.Debug().Msg("lan resolve: sweep throttled")
					break
				}
				if wait > 0 {
					r.sweepMu.Unlock()
					if !r.sleep(ctx, wait) {
						return finish(res)
					}
					continue
				}
				ip, heard, err := r.engine.sweep(ctx, addrs, mac)
				r.remember(heard)
				if err == nil && ctx.Err() == nil {
					// A sweep that failed or was cut short by the
					// context proved nothing either way, so it
					// doesn't count as a miss or schedule a backoff.
					r.noteSweep(mac, ip != nil)
				}
				r.sweepMu.Unlock()
				if err != nil {
					log.Debug().Err(err).Msg("lan resolve: sweep failed")
				} else if ip != nil {
					// One more probe of the address the sweep found, so
					// the engine can seed the kernel's neighbor cache
					// (see arpEngine); the result stands either way.
					delete(tried, ip.String())
					probe(ip, "sweep")
					return finish(Result{IP: ip.String(), Verified: true, Source: "sweep"})
				} else {
					log.Debug().Int("addresses", len(addrs)).Int("heard", len(heard)).Msg("lan resolve: sweep found nothing")
				}
				break
			}
		}
	}

	return finish(res)
}

// Check is the cheap steady-state question: is mac still answering at
// ip? One probe, no sweep. Used by the manager's periodic recheck.
// ok=false with a nil error means something else answered or nothing
// did; an error means the probe couldn't be sent.
func (r *Resolver) Check(ctx context.Context, ip, mac string) (bool, error) {
	if r.engine == nil {
		return false, errNoEngine
	}
	target := net.ParseIP(ip)
	want := NormalizeMAC(mac)
	if target == nil || want == "" {
		return false, errBadTarget
	}
	got, err := r.engine.probe(ctx, target, want)
	if err != nil {
		return false, err
	}
	return got == want, nil
}

// recent returns the address a sweep within seenTTL heard mac from.
func (r *Resolver) recent(mac string) net.IP {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.seen[mac]
	if !ok || r.now().Sub(s.at) > seenTTL {
		return nil
	}
	return s.ip
}

// forget drops a sweep observation that stopped verifying.
func (r *Resolver) forget(mac string) {
	r.mu.Lock()
	delete(r.seen, mac)
	r.mu.Unlock()
}

// remember stores every reply a sweep heard, so cameras resolved after
// this one skip straight to a single probe of the right address.
func (r *Resolver) remember(heard map[string]net.IP) {
	if len(heard) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for mac, ip := range heard {
		r.seen[mac] = seenAddr{ip: ip, at: now}
	}
}

// sweepWait says how long until mac may sweep: 0 means now. backoff is
// true when the camera's own backoff is what's in the way (its last
// sweeps missed), as opposed to the LAN-wide gap after someone else's
// sweep, which is worth waiting out. Call with sweepMu held.
func (r *Resolver) sweepWait(mac string) (wait time.Duration, backoff bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if next, ok := r.camNext[mac]; ok && now.Before(next) {
		return next.Sub(now), true
	}
	if !r.lastSweep.IsZero() {
		if gap := sweepGlobalGap - now.Sub(r.lastSweep); gap > 0 {
			return gap, false
		}
	}
	return 0, false
}

// sleepCtx is the production sleep: true after d, false if ctx ended
// first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// noteSweep records that a sweep ran for mac and schedules the next one
// it may run: right away after a hit, or after a gap that doubles with
// every consecutive miss.
func (r *Resolver) noteSweep(mac string, hit bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.lastSweep = now
	if hit {
		delete(r.camMiss, mac)
		delete(r.camNext, mac)
		return
	}
	misses := r.camMiss[mac] + 1
	r.camMiss[mac] = misses
	gap := sweepCamGap
	for i := 1; i < misses && gap < sweepCamGapMax; i++ {
		gap *= 2
	}
	if gap > sweepCamGapMax {
		gap = sweepCamGapMax
	}
	r.camNext[mac] = now.Add(gap)
}

// misses returns the consecutive sweep misses recorded for mac.
func (r *Resolver) misses(mac string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.camMiss[mac]
}

// clearMisses forgets the miss history, and the sweep backoff that came
// with it, once the camera verified by any rung: a camera that is on
// the LAN right now has earned a prompt sweep the next time it moves.
func (r *Resolver) clearMisses(mac string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.camMiss, mac)
	delete(r.camNext, mac)
}

// without filters addresses already probed out of a sweep list.
func without(ips []net.IP, tried map[string]bool) []net.IP {
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !tried[ip.String()] {
			out = append(out, ip)
		}
	}
	return out
}
