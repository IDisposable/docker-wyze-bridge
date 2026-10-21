package camera

import (
	"context"
	"errors"
	"strings"
)

// Saved video quality: the operator's HD/SD choice per camera, made on
// the Settings page, with the detail page's HD/SD buttons or over MQTT.
// Keyed by MAC like pins and the ignore list, so it survives renames,
// and persisted by main.go through OnQualityChange. A camera with no
// saved choice gets the environment's (QUALITY_<CAM>, then QUALITY).
//
// There is one go2rtc stream per camera, so the choice is what the
// dashboard, Home Assistant, RTSP clients and recordings all get. HD is
// the camera's best: 1080p on a V3, 2K on the 2K models (go2rtc picks).
// SD is 360p at a much lower bitrate.

var (
	// ErrBadQuality means the value isn't hd, sd or empty.
	ErrBadQuality = errors.New("quality must be hd, sd, or empty for the default")
	// ErrQualityNotTUTK means the camera isn't streamed over TUTK, where
	// the bridge asks for a frame size. Doorbells (WebRTC) and Gwell
	// cameras send whatever they send.
	ErrQualityNotTUTK = errors.New("quality only applies to cameras streamed over TUTK")
)

// NormalizeQuality lower-cases and checks a quality value. "" means
// "use the default" and is valid.
func NormalizeQuality(q string) (string, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	switch q {
	case "", "hd", "sd":
		return q, nil
	}
	return "", ErrBadQuality
}

// SetSavedQualities installs the persisted choices (startup, before the
// first Discover). Cameras already in the registry pick theirs up.
func (m *Manager) SetSavedQualities(saved map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.qualities = make(map[string]string, len(saved))
	for mac, q := range saved {
		key := normalizeIgnoreMAC(mac)
		if q, err := NormalizeQuality(q); key != "" && err == nil && q != "" {
			m.qualities[key] = q
		}
	}
	for _, cam := range m.cameras {
		m.applyQualityLocked(cam)
	}
}

// SavedQualities returns a copy of the saved choices by MAC.
func (m *Manager) SavedQualities() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.qualitiesLocked()
}

func (m *Manager) qualitiesLocked() map[string]string {
	out := make(map[string]string, len(m.qualities))
	for k, v := range m.qualities {
		out[k] = v
	}
	return out
}

// SavedQuality is the operator's choice for one camera, "" for none.
func (m *Manager) SavedQuality(cam *Camera) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.qualities[normalizeIgnoreMAC(cam.GetInfo().MAC)]
}

// DefaultQuality is what a camera gets with no saved choice.
func (m *Manager) DefaultQuality(name string) string {
	return m.cfg.CamQuality(name)
}

// OnQualityChange registers the hook for a quality being set: the
// camera, the quality now in effect, and every saved choice (for the
// state file). Fires after each SetQuality, whoever made it, so MQTT can
// tell Home Assistant about a change made on the Settings page.
func (m *Manager) OnQualityChange(fn func(cam *Camera, quality string, saved map[string]string)) {
	m.onQualityChange = fn
}

// applyQualityLocked gives a camera entering the registry its saved
// choice. Call with m.mu held.
func (m *Manager) applyQualityLocked(cam *Camera) {
	if q, ok := m.qualities[normalizeIgnoreMAC(cam.Info.MAC)]; ok {
		cam.SetQuality(q)
	}
}

// SetQuality saves a camera's quality ("" goes back to the default) and,
// if that changes what the camera streams, puts its go2rtc stream back
// with the new frame size and sends its players to reconnect. Returns
// the quality now in effect.
func (m *Manager) SetQuality(ctx context.Context, name, quality string) (string, error) {
	cam := m.GetCamera(name)
	if cam == nil {
		return "", nil // unknown camera: nothing to do, as before
	}
	q, err := NormalizeQuality(quality)
	if err != nil {
		return "", err
	}
	if _, protocol := m.streamSourceFor(cam); protocol != "tutk" {
		return "", ErrQualityNotTUTK
	}
	effective := q
	if effective == "" {
		effective = m.cfg.CamQuality(name)
	}

	var saved map[string]string
	if mac := normalizeIgnoreMAC(cam.GetInfo().MAC); mac != "" {
		m.mu.Lock()
		if m.qualities == nil {
			m.qualities = make(map[string]string)
		}
		if q == "" {
			delete(m.qualities, mac)
		} else {
			m.qualities[mac] = q
		}
		saved = m.qualitiesLocked()
		m.mu.Unlock()
	}

	before := cam.GetQuality()
	cam.SetQuality(effective)
	m.log.Info().Str("cam", name).Str("quality", effective).Bool("default", q == "").Msg("video quality set")
	if m.onQualityChange != nil {
		if saved == nil {
			saved = m.SavedQualities()
		}
		m.onQualityChange(cam, effective, saved)
	}
	if before != effective && cam.GetState() == StateStreaming {
		if go2rtc := m.go2rtcClient(); go2rtc != nil {
			m.registerStream(ctx, go2rtc, cam)
			if cam.GetState() == StateStreaming && m.onStreamRestart != nil {
				m.onStreamRestart(cam, "quality now "+effective)
			}
		}
	}
	return effective, nil
}

// QualityRow is one camera's line on the Settings page.
type QualityRow struct {
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
	Model    string `json:"model"`
	TUTK     bool   `json:"tutk"`    // false: the camera picks its own
	Saved    string `json:"saved"`   // operator's choice, "" for the default
	Default  string `json:"default"` // what "" means for this camera
	Quality  string `json:"quality"` // in effect now
}

// QualityRows lists every camera's quality setting, by name.
func (m *Manager) QualityRows() []QualityRow {
	cams := m.Cameras()
	rows := make([]QualityRow, 0, len(cams))
	for _, cam := range cams {
		info := cam.GetInfo()
		_, protocol := m.streamSourceFor(cam)
		nick := info.Nickname
		if nick == "" {
			nick = cam.Name()
		}
		rows = append(rows, QualityRow{
			Name:     cam.Name(),
			Nickname: nick,
			Model:    info.ModelName(),
			TUTK:     protocol == "tutk",
			Saved:    m.SavedQuality(cam),
			Default:  m.cfg.CamQuality(cam.Name()),
			Quality:  cam.GetQuality(),
		})
	}
	return rows
}
