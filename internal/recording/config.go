// Package recording handles recording configuration and file pruning.
package recording

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
)

// Manager handles recording configuration and pruning.
type Manager struct {
	log  zerolog.Logger
	cfg  *config.Config
}

// NewManager creates a new recording manager.
func NewManager(cfg *config.Config, log zerolog.Logger) *Manager {
	return &Manager{
		log: log,
		cfg: cfg,
	}
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
