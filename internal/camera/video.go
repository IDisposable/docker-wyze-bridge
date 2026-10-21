package camera

import (
	"context"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

// Video is what the bridge actually knows about frames arriving, as
// opposed to State, which only says whether go2rtc accepted the stream.
// go2rtc dials a camera lazily when a consumer pulls and never reports
// a failed dial through its API, so a camera can sit at StateStreaming
// for hours while every pull times out. Two signals close that gap:
// the per-producer byte counters HealthCheck reads every 30s, and the
// stream errors go2rtc prints to its log (RecordStreamError).
type Video int

const (
	VideoUnknown Video = iota // not polled yet, or not streaming
	VideoIdle                 // nobody is pulling; go2rtc hasn't dialed
	VideoLive                 // bytes arrived since the previous poll
	VideoNoVideo              // pulled or connected, but nothing arrived for two polls, or go2rtc reported a stream error
)

// String renders the value the WebUI and /api/cameras use.
func (v Video) String() string {
	switch v {
	case VideoIdle:
		return "idle"
	case VideoLive:
		return "live"
	case VideoNoVideo:
		return "no_video"
	default:
		return "unknown"
	}
}

// stalledPollsForNoVideo is how many consecutive polls without new
// bytes it takes to call a pulled stream dead: a dial takes a few
// seconds and the health poll is 30s, so one quiet poll is normal.
const stalledPollsForNoVideo = 2

// observeFlow folds one health poll into the camera's video status and
// reports the transition. bytes is go2rtc's running total for the
// producer; it drops back toward zero when go2rtc reconnects, which
// counts as fresh flow rather than as nothing arriving.
func (c *Camera) observeFlow(connected bool, consumers int, bytes int, now time.Time) (old, cur Video) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old = c.video
	switch {
	case connected && bytes != c.lastBytes:
		c.video = VideoLive
		c.lastVideo = now
		c.stalledPolls = 0
	case !connected && consumers == 0:
		c.video = VideoIdle
		c.stalledPolls = 0
	default:
		// Someone wants video (or go2rtc is connected) and nothing new
		// arrived. Give it one more poll before calling it.
		c.stalledPolls++
		if c.stalledPolls >= stalledPollsForNoVideo {
			c.video = VideoNoVideo
		}
	}
	c.lastBytes = bytes
	return old, c.video
}

// resetFlow forgets the poll-to-poll bookkeeping when a camera leaves
// StateStreaming; lastVideo and lastError are history and stay.
func (c *Camera) resetFlow() {
	c.video = VideoUnknown
	c.lastBytes = 0
	c.stalledPolls = 0
}

// recordError stores the latest go2rtc stream error. An error is a
// stronger signal than two quiet polls, so the status flips to
// VideoNoVideo at once. Returns the transition.
func (c *Camera) recordError(msg string, at time.Time) (old, cur Video) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = msg
	c.lastErrorAt = at
	old = c.video
	c.video = VideoNoVideo
	return old, c.video
}

// Video returns the current video status under RLock.
func (c *Camera) Video() Video {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.video
}

// OnVideoChange registers a callback for a camera's video status
// changing (live ↔ idle ↔ no video). Fires outside the camera lock.
func (m *Manager) OnVideoChange(fn func(cam *Camera, old, cur Video)) {
	m.onVideoChange = fn
}

// OnStreamError registers a callback for go2rtc reporting an error on
// a camera's stream, after the camera has recorded it.
func (m *Manager) OnStreamError(fn func(cam *Camera, msg string)) {
	m.onStreamError = fn
}

// observeVideo is HealthCheck's per-camera step: fold the stream's
// producer counters into the camera and announce any change.
func (m *Manager) observeVideo(cam *Camera, info *go2rtcmgr.StreamInfo, now time.Time) {
	connected, consumers, bytes := false, 0, 0
	if info != nil {
		connected = info.ProducerConnected()
		consumers = len(info.Consumers)
		bytes = info.ProducerBytes()
	}
	old, cur := cam.observeFlow(connected, consumers, bytes, now)
	if old == cur {
		return
	}
	m.log.Info().
		Str("cam", cam.Name()).
		Str("from", old.String()).
		Str("to", cur.String()).
		Bool("connected", connected).
		Int("consumers", consumers).
		Msg("video status changed")
	if m.onVideoChange != nil {
		m.onVideoChange(cam, old, cur)
	}
}

// errorRecheckGap bounds how often a go2rtc error may trigger a LAN IP
// recheck for the same camera. go2rtc retries a pulled stream every
// few seconds, and one ARP probe a minute is plenty to catch a move.
const errorRecheckGap = time.Minute

// RecordStreamError attributes a go2rtc stream error to a camera: it
// lands on the card and the detail page, flips the video status to
// no_video, and — for a TUTK camera whose discovery timed out — kicks
// an immediate LAN IP recheck instead of waiting for the next
// scheduled one, since a stale address is the usual cause.
func (m *Manager) RecordStreamError(ctx context.Context, name, msg string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}
	now := time.Now()
	old, cur := cam.recordError(msg, now)
	m.log.Warn().Str("cam", name).Str("error", msg).Msg("go2rtc reported a stream error")
	if m.onStreamError != nil {
		m.onStreamError(cam, msg)
	}
	if old != cur && m.onVideoChange != nil {
		m.onVideoChange(cam, old, cur)
	}
	if !strings.Contains(msg, "discovery timeout") {
		return
	}
	if _, protocol := m.streamSourceFor(cam); protocol != "tutk" {
		return
	}
	cam.mu.Lock()
	due := cam.lastErrorRecheck.IsZero() || now.Sub(cam.lastErrorRecheck) >= errorRecheckGap
	if due {
		cam.lastErrorRecheck = now
	}
	cam.mu.Unlock()
	if !due {
		return
	}
	r := m.lanResolver()
	go2rtc := m.go2rtcClient()
	if r == nil || go2rtc == nil {
		return
	}
	m.log.Debug().Str("cam", name).Msg("discovery timeout: rechecking LAN IP now")
	m.recheckLanIP(ctx, r, go2rtc, cam)
}
