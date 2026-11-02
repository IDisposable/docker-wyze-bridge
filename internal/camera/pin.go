package camera

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Pinned addresses: the operator tells the bridge where a TUTK camera
// is and the resolver stays out of it. Meant for the camera ARP can't
// reach — on a VLAN the container doesn't sit on, behind a router
// that doesn't forward broadcasts — where neither the cloud's address
// nor a sweep can be trusted. Keyed by MAC like the ignore list, so a
// pin survives renames; persisted by main.go through OnPinChange.

var (
	// ErrBadPinIP means the address isn't a plain IPv4 address.
	ErrBadPinIP = errors.New("pin must be an IPv4 address like 192.168.4.151")
	// ErrNotLANCamera means the camera isn't dialed on the LAN (WebRTC
	// doorbells, Gwell OG cameras), so an address would mean nothing.
	ErrNotLANCamera = errors.New("only cameras dialed on the LAN (TUTK) can be pinned")
)

// SetPinnedIPs installs the persisted pins (startup, before the first
// Discover). Cameras already in the registry pick theirs up at once.
func (m *Manager) SetPinnedIPs(pins map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pins = make(map[string]string, len(pins))
	for mac, ip := range pins {
		key := normalizeIgnoreMAC(mac)
		if key == "" || net.ParseIP(ip) == nil {
			continue
		}
		m.pins[key] = ip
	}
	for _, cam := range m.cameras {
		cam.SetPinnedIP(m.pins[normalizeIgnoreMAC(cam.Info.MAC)])
	}
}

// PinnedIPs returns a copy of the pins by MAC.
func (m *Manager) PinnedIPs() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pinsLocked()
}

func (m *Manager) pinsLocked() map[string]string {
	out := make(map[string]string, len(m.pins))
	for k, v := range m.pins {
		out[k] = v
	}
	return out
}

// OnPinChange registers the persistence hook: fires with every pin
// after each PinLanIP.
func (m *Manager) OnPinChange(fn func(pins map[string]string)) {
	m.onPinChange = fn
}

// PinLanIP pins a camera to ip, or unpins it when ip is "". The
// effective address changes at once; if the camera is streaming its
// go2rtc stream is re-registered so the next dial goes to the new
// address. Returns the camera's effective address afterwards.
func (m *Manager) PinLanIP(ctx context.Context, name, ip string) (string, error) {
	cam := m.GetCamera(name)
	if cam == nil {
		return "", ErrUnknownCamera
	}
	ip = strings.TrimSpace(ip)
	if ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			return "", ErrBadPinIP
		}
		ip = parsed.To4().String()
	}
	if _, protocol := m.streamSourceFor(cam); protocol != "tutk" {
		return "", ErrNotLANCamera
	}
	mac := normalizeIgnoreMAC(cam.GetInfo().MAC)
	if mac == "" {
		return "", errors.New("camera has no MAC to pin by")
	}

	before := cam.GetInfo().LanIP
	changed := cam.SetPinnedIP(ip)
	m.mu.Lock()
	if m.pins == nil {
		m.pins = make(map[string]string)
	}
	if ip == "" {
		delete(m.pins, mac)
	} else {
		m.pins[mac] = ip
	}
	pins := m.pinsLocked()
	m.mu.Unlock()

	after := cam.GetInfo().LanIP
	if ip == "" {
		m.log.Info().Str("cam", name).Str("ip", after).Msg("camera address unpinned")
	} else {
		m.log.Info().Str("cam", name).Str("ip", ip).Msg("camera address pinned by operator")
	}
	if m.onPinChange != nil {
		m.onPinChange(pins)
	}
	if changed {
		// Same hook the resolver uses, so the dashboard, MQTT and the
		// event log all learn the new address the same way.
		if m.onLanIPChange != nil {
			source := "pinned"
			if ip == "" {
				source = "unpinned"
			}
			m.onLanIPChange(cam, before, after, source)
		}
		if cam.GetState() == StateStreaming {
			if go2rtc := m.go2rtcClient(); go2rtc != nil {
				m.registerStream(ctx, go2rtc, cam)
			}
		}
	}
	return after, nil
}

// applyPinLocked gives a camera entering the registry its persisted
// pin. Call with m.mu held.
func (m *Manager) applyPinLocked(cam *Camera) {
	if ip, ok := m.pins[normalizeIgnoreMAC(cam.Info.MAC)]; ok {
		cam.SetPinnedIP(ip)
	}
}
