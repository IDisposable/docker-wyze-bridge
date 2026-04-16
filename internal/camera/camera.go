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

// Name returns the normalized camera name.
func (c *Camera) Name() string {
	return c.Info.Name
}

// StreamURL returns the go2rtc wyze:// URL for this camera.
func (c *Camera) StreamURL() string {
	return c.Info.StreamURL(c.Quality)
}

// UpdateInfo updates the camera info (e.g., after re-discovery with new IP).
func (c *Camera) UpdateInfo(info wyzeapi.CameraInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Info = info
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
