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
	mu          sync.RWMutex
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
	}
	c.LastSeen = time.Now()
}

// IncrementError increments the error count and returns the new backoff duration.
func (c *Camera) IncrementError() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ErrorCount++
	c.State = StateError
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
// UpdateInfo / SetQuality.
func (c *Camera) StreamURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Info.StreamURL(c.Quality)
}

// UpdateInfo updates the camera info (e.g., after re-discovery with new IP).
func (c *Camera) UpdateInfo(info wyzeapi.CameraInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Info = info
}

// GetInfo returns a consistent snapshot of the Info struct. Use this
// instead of reading c.Info directly when concurrency matters — a
// bare c.Info read can tear against UpdateInfo's assignment.
func (c *Camera) GetInfo() wyzeapi.CameraInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Info
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
}

// Snapshot returns a consistent view of the camera under RLock.
func (c *Camera) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Snapshot{
		Info:        c.Info,
		State:       c.State,
		Quality:     c.Quality,
		AudioOn:     c.AudioOn,
		Record:      c.Record,
		ErrorCount:  c.ErrorCount,
		ConnectedAt: c.ConnectedAt,
		LastSeen:    c.LastSeen,
		ForceWebRTC: c.forceWebRTC,
	}
}

// StatusJSON returns a JSON-friendly status map.
func (c *Camera) StatusJSON() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return map[string]interface{}{
		"name":         c.Info.Name,
		"nickname":     c.Info.Nickname,
		"model":        c.Info.Model,
		"model_name":   c.Info.ModelName(),
		"mac":          c.Info.MAC,
		"ip":           c.Info.LanIP,
		"state":        c.State.String(),
		"quality":      c.Quality,
		"audio":        c.AudioOn,
		"record":       c.Record,
		"fw_version":   c.Info.FWVersion,
		"connected_at": c.ConnectedAt,
		"last_seen":    c.LastSeen,
		"error_count":  c.ErrorCount,
	}
}
