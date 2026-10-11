// Package alerts mirrors Wyze cloud motion/sound events locally: it polls
// get_event_list, downloads each event's snapshot (and clip, when Wyze
// recorded one) before the signed URLs expire, and keeps an index the
// WebUI renders as the Alerts page.
package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// mediaUserAgent is required by prod-sight-safe-auth.wyze.com: unknown
// User-Agents get a misleading 401 "Access token is invalid". The URL's
// signed `st` query is the only credential; no Authorization header.
const mediaUserAgent = "okhttp/4.9.3"

// pageSize is what wyzeapi.GetEventList asks for per call.
const pageSize = 20

// EventLister is satisfied by *wyzeapi.Client.
type EventLister interface {
	GetEventList(macs []string, beginTimeMS, endTimeMS int64) ([]map[string]interface{}, error)
}

// Camera identifies a camera whose events we mirror.
type Camera struct {
	Name     string // normalized bridge name, e.g. front_porch_cam
	Nickname string
	MAC      string
}

// CameraSource lists the current cameras.
type CameraSource func() []Camera

// Alert is one mirrored event.
type Alert struct {
	ID       string   `json:"id"`
	Camera   string   `json:"camera"`
	Nickname string   `json:"nickname"`
	Time     int64    `json:"ts"` // unix ms
	Kind     string   `json:"kind"`
	Tags     []string `json:"tags,omitempty"`
	Image    string   `json:"image,omitempty"` // path relative to Dir
	Clip     string   `json:"clip,omitempty"`  // path relative to Dir
}

// Manager polls Wyze and owns the on-disk alert store.
type Manager struct {
	dir      string
	interval time.Duration
	keep     time.Duration
	api      EventLister
	cameras  CameraSource
	http     *http.Client
	log      zerolog.Logger
	onNew    func(Alert)

	mu     sync.RWMutex
	alerts []Alert // newest first
	seen   map[string]bool
	lastTS int64
}

// NewManager builds a Manager storing under dir (e.g. /media/alerts).
func NewManager(dir string, interval, keep time.Duration, api EventLister, cams CameraSource, log zerolog.Logger) *Manager {
	m := &Manager{
		dir: dir, interval: interval, keep: keep, api: api, cameras: cams, log: log,
		http: &http.Client{Timeout: 60 * time.Second},
		seen: map[string]bool{},
	}
	m.load()
	return m
}

// OnNew registers a callback fired for each newly stored alert.
func (m *Manager) OnNew(fn func(Alert)) { m.onNew = fn }

// Dir is the storage root; Image/Clip paths are relative to it.
func (m *Manager) Dir() string { return m.dir }

// Enabled reports whether polling is on.
func (m *Manager) Enabled() bool { return m.interval > 0 }

// List returns stored alerts, newest first, optionally for one camera.
func (m *Manager) List(camera string, limit int) []Alert {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Alert, 0, len(m.alerts))
	for _, a := range m.alerts {
		if camera != "" && a.Camera != camera {
			continue
		}
		out = append(out, a)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Run polls until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	if !m.Enabled() {
		m.log.Info().Msg("alerts disabled (ALERTS_INTERVAL=0)")
		return
	}
	m.log.Info().Dur("interval", m.interval).Dur("keep", m.keep).Str("dir", m.dir).Msg("alerts mirror started")
	if n := m.BackfillThumbnails(ctx); n > 0 {
		m.log.Info().Int("count", n).Msg("alert thumbnails created from clips")
	}
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		if err := m.Poll(ctx); err != nil {
			m.log.Warn().Err(err).Msg("alerts poll failed")
		}
		m.prune()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll fetches events newer than the last one stored and mirrors them.
func (m *Manager) Poll(ctx context.Context) error {
	cams := m.cameras()
	if len(cams) == 0 {
		return nil
	}
	byMAC := map[string]Camera{}
	macs := make([]string, 0, len(cams))
	for _, c := range cams {
		mac := strings.ToUpper(c.MAC)
		byMAC[mac] = c
		macs = append(macs, mac)
	}

	now := time.Now().UnixMilli()
	m.mu.RLock()
	begin := m.lastTS + 1
	m.mu.RUnlock()
	if floor := now - int64(24*time.Hour/time.Millisecond); begin < floor {
		begin = floor // first run / long outage: last 24h only
	}

	// get_event_list returns up to pageSize events oldest-first; page forward.
	for page := 0; page < 50 && begin < now; page++ {
		events, err := m.api.GetEventList(macs, begin, now)
		if err != nil {
			return err
		}
		maxTS := begin
		for _, raw := range events {
			ev, ok := parseEvent(raw)
			if !ok {
				continue
			}
			if ev.ts > maxTS {
				maxTS = ev.ts
			}
			cam, ok := byMAC[ev.mac]
			if !ok {
				continue
			}
			m.store(ctx, cam, ev)
		}
		if len(events) < pageSize || maxTS <= begin {
			break
		}
		begin = maxTS + 1
	}
	return m.save()
}

type event struct {
	id, mac string
	ts      int64
	value   int
	tags    []int
	image   string
	clips   []string
}

func parseEvent(raw map[string]interface{}) (event, bool) {
	var ev event
	ev.id, _ = raw["event_id"].(string)
	ev.mac, _ = raw["device_id"].(string)
	ev.mac = strings.ToUpper(ev.mac)
	ev.ts = int64(num(raw["event_ts"]))
	ev.value = int(num(raw["event_value"]))
	if tl, ok := raw["tag_list"].([]interface{}); ok {
		for _, t := range tl {
			ev.tags = append(ev.tags, int(num(t)))
		}
	}
	if fl, ok := raw["file_list"].([]interface{}); ok {
		for _, f := range fl {
			fm, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			u, _ := fm["url"].(string)
			if u == "" {
				continue
			}
			if int(num(fm["type"])) == 1 {
				if ev.image == "" {
					ev.image = u
				}
			} else {
				ev.clips = append(ev.clips, u)
			}
			if ev.mac == "" {
				ev.mac = macFromURL(u)
			}
		}
	}
	if ev.id == "" {
		ev.id = fmt.Sprintf("%s_%d", ev.mac, ev.ts)
	}
	return ev, ev.ts > 0 && ev.mac != ""
}

func num(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

// macFromURL extracts the MAC from .../resource/<MAC>/<date>/...
func macFromURL(u string) string {
	_, rest, ok := strings.Cut(u, "/resource/")
	if !ok {
		return ""
	}
	mac, _, _ := strings.Cut(rest, "/")
	return strings.ToUpper(mac)
}

// KindLabel maps event_value to a display label. 1 and 2 match the Wyze
// app's "Motion detected" / "Sound detected"; 13 is what HL_PAN3 reports
// for its cloud-recorded (clip) events, shown by the app as motion.
func KindLabel(v int) string {
	switch v {
	case 1, 13:
		return "motion"
	case 2:
		return "sound"
	case 4, 5:
		return "smoke/co"
	default:
		return "event " + strconv.Itoa(v)
	}
}

// TagLabel maps Wyze AI tags.
func TagLabel(t int) string {
	switch t {
	case 101:
		return "person"
	case 102:
		return "vehicle"
	case 103:
		return "pet"
	case 104:
		return "package"
	default:
		return ""
	}
}

func (m *Manager) store(ctx context.Context, cam Camera, ev event) {
	m.mu.RLock()
	dup := m.seen[ev.id]
	m.mu.RUnlock()
	if dup {
		return
	}
	t := time.UnixMilli(ev.ts)
	kind := KindLabel(ev.value)
	rel := filepath.Join(cam.Name, t.Format("2006-01-02"))
	stem := fmt.Sprintf("%s_%s", t.Format("150405"), strings.ReplaceAll(kind, "/", "-"))
	a := Alert{ID: ev.id, Camera: cam.Name, Nickname: cam.Nickname, Time: ev.ts, Kind: kind}
	for _, tg := range ev.tags {
		if l := TagLabel(tg); l != "" {
			a.Tags = append(a.Tags, l)
		}
	}
	if ev.image != "" {
		p := filepath.Join(rel, stem+".jpg")
		if err := m.download(ctx, ev.image, p, "image/"); err == nil {
			a.Image = p
		} else {
			m.log.Debug().Err(err).Str("cam", cam.Name).Msg("alert snapshot download failed")
		}
	}
	for _, u := range ev.clips { // several variants; first that downloads wins
		p := filepath.Join(rel, stem+".mp4")
		if err := m.download(ctx, u, p, "video/"); err == nil {
			a.Clip = p
			break
		}
	}
	if a.Image == "" && a.Clip != "" {
		// Some HL_PAN3 events ship only a clip; use its first frame.
		p := strings.TrimSuffix(a.Clip, ".mp4") + ".jpg"
		if err := frameFromClip(ctx, filepath.Join(m.dir, a.Clip), filepath.Join(m.dir, p)); err == nil {
			a.Image = p
		} else {
			m.log.Debug().Err(err).Str("cam", cam.Name).Msg("alert thumbnail from clip failed")
		}
	}
	m.mu.Lock()
	m.seen[ev.id] = true
	m.alerts = append(m.alerts, a)
	sort.Slice(m.alerts, func(i, j int) bool { return m.alerts[i].Time > m.alerts[j].Time })
	if ev.ts > m.lastTS {
		m.lastTS = ev.ts
	}
	m.mu.Unlock()
	m.log.Info().Str("cam", cam.Name).Str("kind", kind).Bool("image", a.Image != "").Bool("clip", a.Clip != "").Msg("alert stored")
	if m.onNew != nil {
		m.onNew(a)
	}
}

func (m *Manager) download(ctx context.Context, url, rel, wantType string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, wantType) {
		return fmt.Errorf("unexpected content-type %q", ct)
	}
	full := filepath.Join(m.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 100<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, full)
}

func (m *Manager) indexPath() string { return filepath.Join(m.dir, "index.json") }

func (m *Manager) load() {
	b, err := os.ReadFile(m.indexPath())
	if err != nil {
		return
	}
	var list []Alert
	if json.Unmarshal(b, &list) != nil {
		return
	}
	m.alerts = list
	for _, a := range list {
		m.seen[a.ID] = true
		if a.Time > m.lastTS {
			m.lastTS = a.Time
		}
	}
}

func (m *Manager) save() error {
	m.mu.RLock()
	b, err := json.Marshal(m.alerts)
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	tmp := m.indexPath() + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.indexPath())
}

// prune drops alerts (and their files) older than keep.
func (m *Manager) prune() {
	if m.keep <= 0 {
		return
	}
	cutoff := time.Now().Add(-m.keep).UnixMilli()
	m.mu.Lock()
	kept := m.alerts[:0]
	var drop []Alert
	for _, a := range m.alerts {
		if a.Time < cutoff {
			drop = append(drop, a)
		} else {
			kept = append(kept, a)
		}
	}
	m.alerts = kept
	m.mu.Unlock()
	if len(drop) == 0 {
		return
	}
	for _, a := range drop {
		for _, p := range []string{a.Image, a.Clip} {
			if p != "" {
				os.Remove(filepath.Join(m.dir, p))
			}
		}
	}
	// remove now-empty day directories
	filepath.Walk(m.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && p != m.dir {
			os.Remove(p) // fails harmlessly unless empty
		}
		return nil
	})
	if err := m.save(); err != nil {
		m.log.Warn().Err(err).Msg("alerts index save failed")
	}
	m.log.Info().Int("removed", len(drop)).Msg("alerts pruned")
}

// frameFromClip writes a JPEG of the clip's first second.
func frameFromClip(ctx context.Context, clip, out string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-ss", "0.5", "-i", clip, "-frames:v", "1", "-q:v", "4", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}

// BackfillThumbnails creates snapshots for stored alerts that only have a clip.
func (m *Manager) BackfillThumbnails(ctx context.Context) int {
	m.mu.RLock()
	var todo []int
	for i, a := range m.alerts {
		if a.Image == "" && a.Clip != "" {
			todo = append(todo, i)
		}
	}
	m.mu.RUnlock()
	n := 0
	for _, i := range todo {
		m.mu.RLock()
		a := m.alerts[i]
		m.mu.RUnlock()
		p := strings.TrimSuffix(a.Clip, ".mp4") + ".jpg"
		if frameFromClip(ctx, filepath.Join(m.dir, a.Clip), filepath.Join(m.dir, p)) == nil {
			m.mu.Lock()
			if i < len(m.alerts) && m.alerts[i].ID == a.ID {
				m.alerts[i].Image = p
				n++
			}
			m.mu.Unlock()
		}
	}
	if n > 0 {
		m.save()
	}
	return n
}
