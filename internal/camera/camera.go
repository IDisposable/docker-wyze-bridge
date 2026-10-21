// Package camera manages per-camera state machines and the camera lifecycle.
package camera

import (
	"sync"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// State represents the current state of a camera.
type State int

const (
	StateOffline     State = iota
	StateDiscovering
	StateConnecting
	StateStreaming
	StateError
)

// String returns the human-readable state name.
func (s State) String() string {
	switch s {
	case StateOffline:
		return "offline"
	case StateDiscovering:
		return "discovering"
	case StateConnecting:
		return "connecting"
	case StateStreaming:
		return "streaming"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Camera represents a single managed camera with its state.
type Camera struct {
	Info        wyzeapi.CameraInfo
	State       State
	Quality     string
	AudioOn     bool
	Record      bool
	ConnectedAt time.Time
	LastSeen    time.Time
	ErrorCount  int
	// tutkFailStreak counts consecutive TUTK-path connect failures.
	// Reset on StateStreaming. Once it crosses the manager's threshold
	// the camera flips to forceWebRTC. See DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md.
	tutkFailStreak int
	// forceWebRTC overrides the model registry's routing default and
	// pins this camera to the WebRTC path. Sticky for the process
	// lifetime; a restart re-probes TUTK from scratch.
	forceWebRTC bool
	// lanIP overrides Info.LanIP once the LAN resolver has verified
	// the camera answers at a different address than the Wyze cloud
	// reports (the cloud lags DHCP changes by hours). Empty means the
	// cloud IP is believed. Survives UpdateInfo so a 30-minute refresh
	// can't put the stale address back; the resolver clears it when
	// the cloud catches up. See internal/lanresolve.
	lanIP string
	// How the resolver last did with this camera, for the detail page
	// and /api/cameras: lanSource is the rung that verified the current
	// address ("" until one has), lanVerifiedAt when it last verified
	// (resolve or recheck), lanMisses how many LAN sweeps in a row
	// failed to hear the camera's MAC (0 while it verifies).
	lanSource     string
	lanVerifiedAt time.Time
	lanMisses     int
	// Video flow bookkeeping; see video.go. video/lastBytes/stalledPolls
	// are poll-to-poll state reset whenever the camera leaves
	// StateStreaming. lastVideo, lastError and lastErrorAt are history.
	video            Video
	lastVideo        time.Time
	lastBytes        int
	stalledPolls     int
	lastError        string
	lastErrorAt      time.Time
	lastErrorRecheck time.Time
	mu               sync.RWMutex
}

// NewCamera creates a new Camera from discovered info with default settings.
func NewCamera(info wyzeapi.CameraInfo, quality string, audio, record bool) *Camera {
	return &Camera{
		Info:    info,
		State:   StateOffline,
		Quality: quality,
		AudioOn: audio,
		Record:  record,
	}
}

// GetState returns the current state (thread-safe).
func (c *Camera) GetState() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.State
}

// SetState updates the camera state (thread-safe).
func (c *Camera) SetState(s State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State = s
	if s == StateStreaming {
		c.ConnectedAt = time.Now()
		c.ErrorCount = 0
		c.tutkFailStreak = 0
	} else {
		c.resetFlow()
	}
	c.LastSeen = time.Now()
}

// IncrementError increments the error count and returns the new backoff duration.
func (c *Camera) IncrementError() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ErrorCount++
	c.State = StateError
	c.resetFlow()
	return c.BackoffDuration()
}

// IncrementTUTKFail bumps the TUTK-path failure streak and returns
// the new count. Reset via SetState(StateStreaming) or ResetTUTKFail.
func (c *Camera) IncrementTUTKFail() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tutkFailStreak++
	return c.tutkFailStreak
}

// TUTKFailStreak returns the current consecutive-TUTK-failure count.
func (c *Camera) TUTKFailStreak() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tutkFailStreak
}

// ResetTUTKFail clears the TUTK-failure streak without changing state.
// Called when the routing decision no longer involves TUTK (e.g. after
// promotion to WebRTC) so a later demotion starts from zero.
func (c *Camera) ResetTUTKFail() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tutkFailStreak = 0
}

// ForceWebRTC returns true when this camera has been runtime-promoted
// past the model registry's default routing.
func (c *Camera) ForceWebRTC() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.forceWebRTC
}

// SetForceWebRTC pins (or unpins) this camera to the WebRTC path.
// Idempotent; returns true when the flag actually changed.
func (c *Camera) SetForceWebRTC(on bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.forceWebRTC == on {
		return false
	}
	c.forceWebRTC = on
	return true
}

// BackoffDuration returns the current backoff duration based on error count.
// Formula: min(5s * 2^errorCount, 5min)
func (c *Camera) BackoffDuration() time.Duration {
	d := 5 * time.Second
	for i := 0; i < c.ErrorCount; i++ {
		d *= 2
		if d > 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return d
}

// Name returns the normalized camera name. Safe without locking:
// Info.Name is assigned once at NewCamera time and never mutated —
// UpdateInfo keeps the old Name even when IP/firmware/nickname change.
func (c *Camera) Name() string {
	return c.Info.Name
}

// StreamURL returns the go2rtc wyze:// URL for this camera. Reads
// Info + Quality under the lock so it's consistent with concurrent
// UpdateInfo / SetQuality. Uses the resolved LAN IP when one is set.
func (c *Camera) StreamURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info().StreamURL(c.Quality)
}

// UpdateInfo updates the camera info (e.g., after re-discovery with new IP).
func (c *Camera) UpdateInfo(info wyzeapi.CameraInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Info = info
}

// GetInfo returns a consistent snapshot of the Info struct. Use this
// instead of reading c.Info directly when concurrency matters — a
// bare c.Info read can tear against UpdateInfo's assignment. The
// LanIP field reflects the resolved address when one is set, so every
// consumer (stream URL, WebUI, MQTT, webhooks) agrees on where the
// camera is.
func (c *Camera) GetInfo() wyzeapi.CameraInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info()
}

// info is GetInfo without the lock, for callers that already hold it.
func (c *Camera) info() wyzeapi.CameraInfo {
	info := c.Info
	if c.lanIP != "" {
		info.LanIP = c.lanIP
	}
	return info
}

// LanIPs returns the address the Wyze cloud reports and the one the
// LAN resolver verified ("" when none, or when they agree).
func (c *Camera) LanIPs() (cloud, resolved string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Info.LanIP, c.lanIP
}

// SetLanIP records (or with "" clears) the resolver-verified address.
// A value equal to the cloud IP is stored as "" since there is nothing
// to override. Returns true when the effective address — what GetInfo
// and StreamURL report — actually changed.
func (c *Camera) SetLanIP(ip string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := c.info().LanIP
	if ip == c.Info.LanIP {
		ip = ""
	}
	c.lanIP = ip
	return c.info().LanIP != before
}

// lanAbsentAfter is how many LAN sweeps in a row have to miss a camera
// before the dashboard suggests it isn't on this LAN at all. One miss
// can be a camera rebooting; two full sweeps a few minutes apart with
// no answer means it's somewhere else (another house on the same Wyze
// account, typically).
const lanAbsentAfter = 2

// lanAbsent is the "probably not on this LAN" verdict; see lanAbsentAfter.
func (c *Camera) lanAbsent() bool {
	return c.lanMisses >= lanAbsentAfter
}

// LanStatus returns the resolver's history for this camera: the rung
// that verified its address ("" if none ever has), when it last
// verified, and the sweeps in a row that failed to hear it.
func (c *Camera) LanStatus() (source string, verifiedAt time.Time, misses int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lanSource, c.lanVerifiedAt, c.lanMisses
}

// noteLanVerified records a successful verification by source, or by
// the periodic recheck when source is "" (the rung stays as it was).
func (c *Camera) noteLanVerified(source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if source != "" {
		c.lanSource = source
	}
	c.lanVerifiedAt = time.Now()
	c.lanMisses = 0
}

// noteLanMisses records that the resolver couldn't verify the camera
// and how many sweeps have now failed to find it. The source and
// verified-at history stay: "last verified 3h ago via sweep" is useful
// context for a camera that is missing now.
func (c *Camera) noteLanMisses(misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lanMisses = misses
}

// GetQuality returns the current quality setting under RLock.
func (c *Camera) GetQuality() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Quality
}

// SetQuality replaces the quality setting atomically.
func (c *Camera) SetQuality(q string) {
	c.mu.Lock()
	c.Quality = q
	c.mu.Unlock()
}

// GetAudioOn returns the current audio-on setting under RLock.
func (c *Camera) GetAudioOn() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AudioOn
}

// SetAudioOn replaces the audio-on setting atomically.
func (c *Camera) SetAudioOn(on bool) {
	c.mu.Lock()
	c.AudioOn = on
	c.mu.Unlock()
}

// GetErrorCount returns the current error count under RLock.
func (c *Camera) GetErrorCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ErrorCount
}

// Snapshot is a consistent multi-field view. Use for places that
// render several fields together (metrics page, MQTT publish, HTML
// templates) and want a single atomic capture rather than several
// individually-locked reads that could disagree.
type Snapshot struct {
	Info        wyzeapi.CameraInfo
	State       State
	Quality     string
	AudioOn     bool
	Record      bool
	ErrorCount  int
	ConnectedAt time.Time
	LastSeen    time.Time
	// ForceWebRTC reflects the runtime TUTK→WebRTC fallback. Rendered
	// as `protocol_forced` in /api/metrics so operators can tell the
	// registry default from the promoted-at-runtime state.
	ForceWebRTC bool
	// Video is whether frames are actually arriving (see video.go);
	// LastVideo is when they last did, LastError the most recent go2rtc
	// stream error and when it was reported. Zero values when unknown.
	Video       Video
	LastVideo   time.Time
	LastError   string
	LastErrorAt time.Time
	// LanSource / LanVerifiedAt / LanMisses are the LAN resolver's
	// history for the camera; see Camera.LanStatus.
	LanSource     string
	LanVerifiedAt time.Time
	LanMisses     int
	// LanAbsent is true once enough sweeps in a row missed the camera
	// that it's probably not on this LAN (see lanAbsentAfter).
	LanAbsent bool
}

// Snapshot returns a consistent view of the camera under RLock.
func (c *Camera) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Info:          c.info(),
		State:         c.State,
		Quality:       c.Quality,
		AudioOn:       c.AudioOn,
		Record:        c.Record,
		ErrorCount:    c.ErrorCount,
		ConnectedAt:   c.ConnectedAt,
		LastSeen:      c.LastSeen,
		ForceWebRTC:   c.forceWebRTC,
		Video:         c.video,
		LastVideo:     c.lastVideo,
		LastError:     c.lastError,
		LastErrorAt:   c.lastErrorAt,
		LanSource:     c.lanSource,
		LanVerifiedAt: c.lanVerifiedAt,
		LanMisses:     c.lanMisses,
		LanAbsent:     c.lanAbsent(),
	}
}

// StatusJSON returns a JSON-friendly status map.
func (c *Camera) StatusJSON() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return map[string]interface{}{
		"name":          c.Info.Name,
		"nickname":      c.Info.Nickname,
		"model":         c.Info.Model,
		"model_name":    c.Info.ModelName(),
		"mac":           c.Info.MAC,
		"ip":            c.info().LanIP,
		"cloud_ip":      c.Info.LanIP,
		"state":         c.State.String(),
		"quality":       c.Quality,
		"audio":         c.AudioOn,
		"record":        c.Record,
		"fw_version":    c.Info.FWVersion,
		"connected_at":  c.ConnectedAt,
		"last_seen":     c.LastSeen,
		"error_count":   c.ErrorCount,
		"video":         c.video.String(),
		"last_video":    c.lastVideo,
		"last_error":    c.lastError,
		"last_error_at": c.lastErrorAt,
		// LAN resolver history: which rung verified the address, when it
		// last verified, and how many sweeps in a row missed the camera.
		"lan_ip_source":      c.lanSource,
		"lan_ip_verified_at": c.lanVerifiedAt,
		"lan_sweep_misses":   c.lanMisses,
		"lan_absent":         c.lanAbsent(),
	}
}
