// Package recording handles recording configuration and file pruning.
package recording

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/issues"
)

// StateChangeFn is called whenever a camera's recording status flips.
// Wired into the MQTT publisher and the metrics page's SSE stream so
// downstream consumers don't have to poll IsRecording.
type StateChangeFn func(camName string, recording bool)

// Manager handles recording configuration, ffmpeg supervision, and
// file pruning.
type Manager struct {
	log       zerolog.Logger
	cfg       *config.Config
	issues    *issues.Registry // optional; nil = no surfacing
	mu        *sync.Mutex
	recorders map[string]*recorder // keyed by camera name
	onChange  StateChangeFn        // nil = no callback
}

// NewManager creates a new recording manager. Pass a non-nil issues
// registry to surface configuration problems (bad RECORD_PATH
// templates, ffmpeg errors) on /api/health and /metrics instead of
// just logging.
func NewManager(cfg *config.Config, iss *issues.Registry, log zerolog.Logger) *Manager {
	return &Manager{
		log:       log,
		cfg:       cfg,
		issues:    iss,
		mu:        &sync.Mutex{},
		recorders: map[string]*recorder{},
	}
}

// OnChange registers a single callback fired when a camera's recording
// state flips on or off. Replace, don't stack — there's exactly one
// consumer (the wiring in main.go that fans out to MQTT + SSE).
func (m *Manager) OnChange(fn StateChangeFn) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// IsRecording reports whether an ffmpeg supervisor is currently active
// for the named camera. Cheap — just a map lookup under the shared
// mutex.
func (m *Manager) IsRecording(camName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.recorders[camName]
	return ok
}

// ActiveRecorders returns the names of all cameras currently being
// recorded. Snapshot; callers must not assume the set is stable across
// calls.
func (m *Manager) ActiveRecorders() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.recorders))
	for name := range m.recorders {
		out = append(out, name)
	}
	return out
}

// SessionBytes returns the size in bytes of the currently-open MP4
// segment for a camera (0 if not recording or the file doesn't exist
// yet). Lets the UI show "recording: 4.2 MB so far" without having to
// scrape ffmpeg stderr.
func (m *Manager) SessionBytes(camName string) int64 {
	target, _ := m.buildFFmpegArgs(camName)
	// target is the segment template with %Y/%m/… strftime. We don't
	// know ffmpeg's current segment filename from here, so walk the
	// expanded directory and sum the newest .mp4 — good enough for the
	// "is it growing?" signal the UI needs.
	dir := filepath.Dir(target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var newest os.FileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if filepath.Ext(info.Name()) != ".mp4" {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest = info
		}
	}
	if newest == nil {
		return 0
	}
	return newest.Size()
}

// RecordPathForCamera returns the resolved recording path for a camera.
func (m *Manager) RecordPathForCamera(camName string) string {
	path := m.cfg.RecordPath
	path = strings.ReplaceAll(path, "{cam_name}", camName)
	path = strings.ReplaceAll(path, "{CAM_NAME}", strings.ToUpper(camName))
	return path
}

// RecordFileNameForCamera returns the go2rtc-compatible record path template.
func (m *Manager) RecordFileNameForCamera(camName string) string {
	dir := m.RecordPathForCamera(camName)
	filename := m.cfg.RecordFileName

	combined := filepath.Join(dir, filename)

	// Validate: must contain %s OR all of %Y %m %d %H %M %S
	hasUnix := strings.Contains(combined, "%s")
	hasAll := strings.Contains(combined, "%Y") &&
		strings.Contains(combined, "%m") &&
		strings.Contains(combined, "%d") &&
		strings.Contains(combined, "%H") &&
		strings.Contains(combined, "%M") &&
		strings.Contains(combined, "%S")

	if !hasUnix && !hasAll {
		m.log.Warn().
			Str("path", combined).
			Msg("recording path missing time variables, appending _%s")
		if m.issues != nil {
			m.issues.Report(issues.Issue{
				ID:       "config/record_path/" + camName,
				Severity: issues.SeverityWarn,
				Scope:    "config",
				Camera:   camName,
				Message:  "RECORD_PATH / RECORD_FILE_NAME missing time variables — segments would overwrite; bridge appended _%s automatically",
				RawValue: combined,
			})
		}
		combined += "_%s"
	}

	return combined
}

// IsEnabled returns true if recording is enabled for a camera.
func (m *Manager) IsEnabled(camName string) bool {
	return m.cfg.CamRecord(camName)
}

// RunPruner starts the recording pruning loop.
func (m *Manager) RunPruner(ctx context.Context) {
	if m.cfg.RecordKeep <= 0 {
		m.log.Debug().Msg("recording pruning disabled (keep=0)")
		return
	}

	m.log.Info().
		Dur("keep", m.cfg.RecordKeep).
		Msg("recording pruner started")

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.prune()
		}
	}
}

func (m *Manager) prune() {
	cutoff := time.Now().Add(-m.cfg.RecordKeep)
	baseDir := strings.ReplaceAll(m.cfg.RecordPath, "{cam_name}", "")
	baseDir = strings.ReplaceAll(baseDir, "{CAM_NAME}", "")

	// Walk from the root of the recording path
	// Find the common prefix before any template variables
	parts := strings.SplitN(baseDir, "%", 2)
	walkDir := strings.TrimRight(parts[0], "/")
	if walkDir == "" {
		walkDir = "/record"
	}

	var deleted int
	var freedBytes int64
	var emptyDirs []string

	err := filepath.Walk(walkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			emptyDirs = append(emptyDirs, path)
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".mp4" {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			freedBytes += info.Size()
			if err := os.Remove(path); err == nil {
				deleted++
				m.log.Debug().Str("file", path).Msg("pruned recording")
			}
		}
		return nil
	})

	if err != nil {
		m.log.Warn().Err(err).Msg("recording prune walk error")
	}

	// Clean up empty directories (reverse order for depth-first)
	for i := len(emptyDirs) - 1; i >= 0; i-- {
		dir := emptyDirs[i]
		if dir == walkDir {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			os.Remove(dir)
		}
	}

	if deleted > 0 {
		m.log.Info().
			Int("deleted", deleted).
			Str("freed", formatBytes(freedBytes)).
			Msg("recording pruning complete")
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
