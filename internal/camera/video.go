package camera

import (
	"context"
	"errors"
	"regexp"
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
		// Video arriving heals whatever recordError counted; lastError
		// stays as history.
		c.dialFailures = 0
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
//
// A failed dial also counts: dialFailures is part of the error_count
// /api/cameras and the WebUI show, and a camera whose every dial
// times out reading error_count 0 is the lie this stops. The state
// machine and its own ErrorCount are left alone on purpose: a log
// line attributed to this camera shouldn't be able to knock a working
// stream out of StateStreaming, or stretch its reconnect backoff, by
// itself.
func (c *Camera) recordError(msg string, at time.Time) (old, cur Video) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = msg
	c.lastErrorAt = at
	if ConnectError(msg) {
		c.dialFailures++
	}
	old = c.video
	c.video = VideoNoVideo
	return old, c.video
}

// AddPuller counts a browser socket for this stream opening (+1) or
// closing (-1); see Camera.pullers.
func (c *Camera) AddPuller(delta int) {
	c.mu.Lock()
	c.pullers += delta
	if c.pullers < 0 {
		c.pullers = 0
	}
	c.mu.Unlock()
}

// Pullers returns how many browser sockets are open for the stream.
func (c *Camera) Pullers() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pullers
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

// OnStreamRestart registers a callback for the bridge having replaced a
// camera's go2rtc stream, with the reason (its connection went silent,
// say). Whoever is still attached to the old stream has to reconnect to
// reach the new one; the WebUI drops its players' sockets so they do.
func (m *Manager) OnStreamRestart(fn func(cam *Camera, reason string)) {
	m.onStreamRestart = fn
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
		// Count the video track alone where go2rtc says which track is
		// which, so audio still flowing can't pass for video. When it
		// doesn't say (not dialed yet, or an older go2rtc) the producer
		// total is the honest fallback rather than reading zero as dead.
		if vb, known := info.VideoBytes(); known {
			bytes = vb
		} else {
			bytes = info.ProducerBytes()
		}
	}
	// A browser waiting on the /ws proxy is a consumer go2rtc doesn't
	// list until it can feed it.
	if p := cam.Pullers(); p > consumers {
		consumers = p
	}
	old, cur := cam.observeFlow(connected, consumers, bytes, now)
	m.restartIfFrozen(cam, cur, connected, consumers, now)
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

// frozenRestartGap is the least time between two restarts of the same
// camera's stream, so a camera that keeps going silent is retried every
// couple of minutes rather than on every poll.
const frozenRestartGap = 2 * time.Minute

// restartIfFrozen replaces a TUTK stream whose connection to the camera
// is up but has carried nothing for two polls while someone waits on
// it. go2rtc keeps such a session open indefinitely: a V3 that drops
// off Wi-Fi mid-stream leaves a producer with a remote address and
// frozen byte counters, every new viewer joins that same dead producer,
// and only Restart Stream brings the picture back. This does the same,
// at most once per frozenRestartGap.
//
// A stream that is pulled but not yet connected is left alone: go2rtc
// is still dialing it, and replacing streams mid-dial is churn the
// cameras don't need.
func (m *Manager) restartIfFrozen(cam *Camera, cur Video, connected bool, consumers int, now time.Time) {
	if cur != VideoNoVideo || !connected || consumers == 0 {
		return
	}
	if _, protocol := m.streamSourceFor(cam); protocol != "tutk" {
		return
	}
	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return
	}
	cam.mu.Lock()
	due := cam.lastRestart.IsZero() || now.Sub(cam.lastRestart) >= frozenRestartGap
	if due {
		cam.lastRestart = now
	}
	cam.mu.Unlock()
	if !due {
		return
	}
	m.log.Warn().Str("cam", cam.Name()).Int("consumers", consumers).
		Msg("stream connected but silent; restarting it")
	go func() {
		m.registerStream(context.Background(), go2rtc, cam)
		if cam.GetState() == StateStreaming && m.onStreamRestart != nil {
			m.onStreamRestart(cam, "connected but silent")
		}
	}()
}

// consumerSideError reports go2rtc errors about what a consumer asked
// for rather than about the camera (a snapshot wanting JPEG from an
// H264 stream, say). They don't say anything about video arriving.
func consumerSideError(msg string) bool {
	return strings.Contains(msg, "codecs not matched")
}

// ConnectError reports a go2rtc error that means the dial to the
// camera itself failed. Those count against the camera; anything else
// is recorded and left at that.
func ConnectError(msg string) bool {
	return strings.Contains(msg, "connect failed") ||
		strings.Contains(msg, "discovery timeout") ||
		strings.Contains(msg, "no answer")
}

// sourceWordRE splits a producer source into the words a stream name
// could be: rtsp://127.0.0.1:8554/driveway_bedb?video, ffmpeg:
// front_door#video=mjpeg and the -i inside an exec: line all carry the
// name between punctuation. Camera names are \w and - only, so a
// camera called bedb can't match inside driveway_bedb.
var sourceWordRE = regexp.MustCompile(`[\w-]+`)

// noteCopyStreams records which go2rtc streams only re-serve a
// camera's stream. A dashboard panel or a recording leg sits in the
// table under its own name (panel_driveway_bedb) with the camera's
// stream as its source, and go2rtc reports a failed dial against
// whichever stream it was serving at the time; without this the error
// lands on a name no camera answers to and is dropped. Rebuilt from
// the whole table on every health poll, so it follows streams coming
// and going.
func (m *Manager) noteCopyStreams(streams map[string]*go2rtcmgr.StreamInfo) {
	m.mu.RLock()
	names := make(map[string]bool, len(m.cameras))
	for name := range m.cameras {
		names[name] = true
	}
	m.mu.RUnlock()

	copies := make(map[string]string)
	for streamName, info := range streams {
		if streamName == "" || info == nil || names[streamName] {
			continue
		}
		for _, p := range info.Producers {
			cam := ""
			for _, word := range sourceWordRE.FindAllString(p.SourceURL(), -1) {
				if names[word] {
					cam = word
					break
				}
			}
			if cam != "" {
				copies[streamName] = cam
				break
			}
		}
	}

	m.mu.Lock()
	m.copies = copies
	m.mu.Unlock()
}

// resolveStreamName maps a go2rtc stream name to the camera whose
// video it carries, "" when none. A camera's own name first, then the
// copies learned at the last health poll, then a name that ends in
// "_<camera>" so a copy made since that poll still lands
// (panel_driveway_bedb → driveway_bedb).
func (m *Manager) resolveStreamName(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.cameras[name]; ok {
		return name
	}
	if cam, ok := m.copies[name]; ok {
		return cam
	}
	best := ""
	for cam := range m.cameras {
		if len(cam) > len(best) && strings.HasSuffix(name, "_"+cam) {
			best = cam
		}
	}
	return best
}

// RecordSnapshotError reports a snapshot go2rtc couldn't serve. go2rtc
// dials the camera for a frame, so when it answers with an error that
// is the camera not answering, and it reaches go2rtc's log as an
// [api.mjpeg] line with no stream= token to attribute it by. The
// caller knows which camera it asked for, so it lands there. Only
// go2rtc's own answer counts: our client giving up first (10s, and a
// dozing V3 can take 40s to send a frame) or the browser cancelling
// says nothing about the camera.
func (m *Manager) RecordSnapshotError(ctx context.Context, name string, err error) {
	var answer *go2rtcmgr.StatusError
	if err == nil || !errors.As(err, &answer) {
		m.log.Debug().Err(err).Str("cam", name).Msg("snapshot failed without an answer from go2rtc; not recorded")
		return
	}
	m.RecordStreamError(ctx, name, "snapshot: "+strings.TrimPrefix(answer.Body, "streams: "))
}

// RecordStreamError attributes a go2rtc stream error to a camera: it
// lands on the card and the detail page, flips the video status to
// no_video, and — for a TUTK camera whose discovery timed out — kicks
// an immediate LAN IP recheck instead of waiting for the next
// scheduled one, since a stale address is the usual cause. The name
// may be a copy of a camera's stream; see resolveStreamName.
func (m *Manager) RecordStreamError(ctx context.Context, name, msg string) {
	resolved := m.resolveStreamName(name)
	if resolved == "" {
		m.log.Debug().Str("stream", name).Str("error", msg).Msg("go2rtc stream error for a stream that isn't a camera's")
		return
	}
	cam := m.GetCamera(resolved)
	if cam == nil {
		return
	}
	if resolved != name {
		m.log.Debug().Str("stream", name).Str("cam", resolved).Msg("go2rtc stream error on a copy; attributing it to the camera")
		name = resolved
	}
	if consumerSideError(msg) {
		m.log.Debug().Str("cam", name).Str("error", msg).Msg("go2rtc consumer error (not the camera's)")
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
