package camera

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// The ignore list is the runtime, WebUI-editable counterpart of
// FILTER_NAMES / FILTER_MACS. The env filters still apply first; the
// ignore list drops cameras on top of them. It exists because a Wyze
// account often sees cameras that aren't on this LAN at all — a
// relative's house, a shared camera — and each of those costs a
// reconnect attempt every few minutes, a LAN sweep now and then, and a
// card on the dashboard, forever. Ignoring is keyed by MAC so a
// renamed camera stays ignored, and it persists through the state
// file so a restart doesn't bring the noise back.

var (
	// ErrUnknownCamera is returned by Ignore for a name the manager
	// doesn't have (already ignored, filtered out, or never discovered).
	ErrUnknownCamera = errors.New("unknown camera")
	// ErrNotIgnored is returned by Unignore for a MAC not on the list.
	ErrNotIgnored = errors.New("camera is not ignored")
)

// SetIgnored installs the persisted ignore list. Meant to be called once
// before the first Discover; later edits go through Ignore/Unignore.
func (m *Manager) SetIgnored(list []wyzeapi.IgnoredCamera) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ignored = make(map[string]wyzeapi.IgnoredCamera, len(list))
	for _, ic := range list {
		mac := normalizeIgnoreMAC(ic.MAC)
		if mac == "" {
			continue
		}
		ic.MAC = mac
		m.ignored[mac] = ic
	}
}

// Ignored returns a copy of the ignore list, sorted by nickname then
// MAC so the WebUI renders it in a stable order.
func (m *Manager) Ignored() []wyzeapi.IgnoredCamera {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ignoredLocked()
}

func (m *Manager) ignoredLocked() []wyzeapi.IgnoredCamera {
	out := make([]wyzeapi.IgnoredCamera, 0, len(m.ignored))
	for _, ic := range m.ignored {
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Nickname != out[j].Nickname {
			return out[i].Nickname < out[j].Nickname
		}
		return out[i].MAC < out[j].MAC
	})
	return out
}

// IsIgnored reports whether a MAC (any spelling) is on the list.
func (m *Manager) IsIgnored(mac string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.ignored[normalizeIgnoreMAC(mac)]
	return ok
}

// OnIgnoreChange registers the persistence hook: fires with the full
// list after every Ignore / Unignore.
func (m *Manager) OnIgnoreChange(fn func(list []wyzeapi.IgnoredCamera)) {
	m.onIgnoreChange = fn
}

// OnCameraAdded registers a callback for a camera entering the
// registry (first discovery, or restored from the ignore list).
func (m *Manager) OnCameraAdded(fn func(cam *Camera)) {
	m.onAdded = fn
}

// OnCameraRemoved registers a callback for a camera leaving the
// registry entirely — ignored by the operator, or reaped after a
// rename. Fires after the camera is gone, so observers that enumerate
// Cameras() won't see it; the *Camera is handed over for its name and
// MAC. reason is "ignored" or "renamed".
func (m *Manager) OnCameraRemoved(fn func(cam *Camera, reason string)) {
	m.onRemoved = fn
}

// Ignore drops a camera from the registry and remembers its MAC so
// discovery keeps dropping it. The go2rtc stream is deleted and the
// camera is marked offline first so recording, MQTT state and
// webhooks see a normal shutdown before the removal callback fires.
func (m *Manager) Ignore(ctx context.Context, name string) (wyzeapi.IgnoredCamera, error) {
	cam := m.GetCamera(name)
	if cam == nil {
		return wyzeapi.IgnoredCamera{}, ErrUnknownCamera
	}
	info := cam.GetInfo()
	mac := normalizeIgnoreMAC(info.MAC)
	if mac == "" {
		return wyzeapi.IgnoredCamera{}, errors.New("camera has no MAC to ignore by")
	}
	entry := wyzeapi.IgnoredCamera{
		MAC:      mac,
		Nickname: info.Nickname,
		Model:    info.Model,
		Since:    time.Now(),
	}

	if go2rtc := m.go2rtcClient(); go2rtc != nil {
		_ = go2rtc.DeleteStream(ctx, name)
	}
	m.changeState(cam, StateOffline)

	m.mu.Lock()
	m.ignored[mac] = entry
	delete(m.cameras, name)
	list := m.ignoredLocked()
	m.mu.Unlock()

	m.log.Info().
		Str("cam", name).
		Str("mac", mac).
		Str("model", info.ModelName()).
		Msg("camera ignored by operator")
	if m.onRemoved != nil {
		m.onRemoved(cam, "ignored")
	}
	if m.onIgnoreChange != nil {
		m.onIgnoreChange(list)
	}
	return entry, nil
}

// Unignore takes a MAC off the list. The camera comes back on the next
// Discover — callers that want it back right away run one.
func (m *Manager) Unignore(mac string) (wyzeapi.IgnoredCamera, error) {
	key := normalizeIgnoreMAC(mac)
	m.mu.Lock()
	entry, ok := m.ignored[key]
	if ok {
		delete(m.ignored, key)
	}
	list := m.ignoredLocked()
	m.mu.Unlock()
	if !ok {
		return wyzeapi.IgnoredCamera{}, ErrNotIgnored
	}
	m.log.Info().Str("mac", key).Str("nickname", entry.Nickname).Msg("camera restored from ignore list")
	if m.onIgnoreChange != nil {
		m.onIgnoreChange(list)
	}
	return entry, nil
}

// dropIgnored removes ignored cameras from a discovery result and, as
// a side effect, refreshes the nickname/model the ignore list shows
// for them — a camera renamed in the Wyze app stays recognizable.
func (m *Manager) dropIgnored(cameras []wyzeapi.CameraInfo) []wyzeapi.CameraInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ignored) == 0 {
		return cameras
	}
	kept := cameras[:0]
	for _, cam := range cameras {
		mac := normalizeIgnoreMAC(cam.MAC)
		entry, ok := m.ignored[mac]
		if !ok {
			kept = append(kept, cam)
			continue
		}
		if entry.Nickname != cam.Nickname || entry.Model != cam.Model {
			entry.Nickname, entry.Model = cam.Nickname, cam.Model
			m.ignored[mac] = entry
		}
		m.log.Debug().Str("cam", cam.NormalizedName()).Str("mac", mac).Msg("skipping ignored camera")
	}
	return kept
}

// normalizeIgnoreMAC matches CameraInfo.MAC's spelling (uppercase, as
// the Wyze cloud sends it) after trimming whitespace, so a MAC typed
// into the API in either case finds the entry.
func normalizeIgnoreMAC(mac string) string {
	return strings.ToUpper(strings.TrimSpace(mac))
}
