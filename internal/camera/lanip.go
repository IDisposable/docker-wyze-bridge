package camera

import (
	"context"
	"sync"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
)

// LANResolver is the slice of internal/lanresolve the manager uses.
// It's an interface so tests can script answers without a network.
type LANResolver interface {
	// Resolve finds the address the camera's MAC is answering from.
	Resolve(ctx context.Context, t lanresolve.Target) lanresolve.Result
	// Check asks whether mac still answers at ip. One probe, no sweep.
	Check(ctx context.Context, ip, mac string) (bool, error)
}

// SetLANResolver attaches the resolver that verifies TUTK cameras' LAN
// addresses before they're handed to go2rtc. nil (the default) keeps
// the pre-resolver behavior: the Wyze cloud's IP is used as-is.
func (m *Manager) SetLANResolver(r LANResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = r
}

// OnLanIPChange registers a callback for the effective LAN address of
// a camera changing — either corrected away from a stale cloud IP, or
// reverted once the cloud caught up. Fires after the camera already
// reports the new address, so callbacks may read cam.GetInfo().
func (m *Manager) OnLanIPChange(fn func(cam *Camera, oldIP, newIP, source string)) {
	m.onLanIPChange = fn
}

func (m *Manager) lanResolver() LANResolver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.resolver
}

// resolveLanIP verifies where a TUTK camera is before go2rtc dials it
// and updates the camera's effective address when the LAN disagrees
// with the cloud. Returns true when the effective address changed.
//
// An unverified result (camera unreachable, no engine) leaves the
// camera exactly as it was: the last verified override if there is
// one, otherwise the cloud IP — never worse than the old behavior.
func (m *Manager) resolveLanIP(ctx context.Context, cam *Camera) bool {
	r := m.lanResolver()
	if r == nil {
		return false
	}
	cloud, resolved := cam.LanIPs()
	info := cam.GetInfo()
	res := r.Resolve(ctx, lanresolve.Target{
		Name:    cam.Name(),
		MAC:     info.MAC,
		CloudIP: cloud,
		LastIP:  resolved,
	})
	log := m.log.With().
		Str("cam", cam.Name()).
		Str("cloud_ip", cloud).
		Str("source", res.Source).
		Dur("took", res.Elapsed).
		Logger()
	if !res.Verified {
		cam.noteLanMisses(res.Misses)
		log.Warn().Str("using", info.LanIP).Int("sweep_misses", res.Misses).Msg("camera not found on LAN, using last known address")
		return false
	}
	cam.noteLanVerified(res.Source)
	before := info.LanIP
	if !cam.SetLanIP(res.IP) {
		log.Debug().Str("ip", res.IP).Msg("camera LAN IP verified")
		return false
	}
	after := cam.GetInfo().LanIP
	if after == cloud {
		log.Info().Str("old_ip", before).Msg("Wyze cloud caught up with the camera's LAN IP")
	} else {
		log.Info().Str("old_ip", before).Str("ip", after).Msg("camera LAN IP differs from Wyze cloud, corrected")
	}
	if m.onLanIPChange != nil {
		m.onLanIPChange(cam, before, after, res.Source)
	}
	return true
}

// RecheckLanIPs asks, for every streaming TUTK camera, whether it still
// answers at the address go2rtc was given. go2rtc only dials when a
// consumer pulls the stream and never reports a failed dial back to
// us, so without this a camera that changed address mid-run would sit
// at "streaming" with a dead URL until the next restart. A camera that
// answers costs one ARP round trip; one that doesn't gets the full
// resolver ladder, and only a verified different address causes the
// stream to be re-registered — an unreachable camera is left alone so
// a battery camera asleep in the yard doesn't flap.
func (m *Manager) RecheckLanIPs(ctx context.Context) {
	r := m.lanResolver()
	go2rtc := m.go2rtcClient()
	if r == nil || go2rtc == nil {
		return
	}

	m.mu.RLock()
	var cams []*Camera
	for _, c := range m.cameras {
		if c.GetState() != StateStreaming {
			continue
		}
		if _, protocol := m.streamSourceFor(c); protocol != "tutk" {
			continue
		}
		cams = append(cams, c)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, cam := range cams {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(c *Camera) {
			defer wg.Done()
			m.recheckLanIP(ctx, r, go2rtc, c)
		}(cam)
	}
	wg.Wait()
}

func (m *Manager) recheckLanIP(ctx context.Context, r LANResolver, go2rtc *go2rtcmgr.APIClient, cam *Camera) {
	info := cam.GetInfo()
	ok, err := r.Check(ctx, info.LanIP, info.MAC)
	if err != nil {
		m.log.Debug().Err(err).Str("cam", cam.Name()).Str("ip", info.LanIP).Msg("lan ip recheck skipped")
		return
	}
	if ok {
		cam.noteLanVerified("")
		return
	}
	m.log.Debug().Str("cam", cam.Name()).Str("ip", info.LanIP).Msg("camera no longer answers at its address, resolving")
	if !m.resolveLanIP(ctx, cam) {
		return
	}
	m.log.Info().
		Str("cam", cam.Name()).
		Str("old_ip", info.LanIP).
		Str("ip", cam.GetInfo().LanIP).
		Msg("LAN IP changed while streaming, re-registering with go2rtc")
	m.registerStream(ctx, go2rtc, cam)
}
