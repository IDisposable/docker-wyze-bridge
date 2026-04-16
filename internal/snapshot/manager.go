// Package snapshot handles periodic camera snapshots and file management.
package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nathan-osman/go-sunrise"
	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

// SnapshotFunc is called after a snapshot is captured for a camera.
type SnapshotFunc func(camName string, jpeg []byte)

// Manager handles periodic snapshot capture.
type Manager struct {
	log       zerolog.Logger
	cfg       *config.Config
	camMgr    *camera.Manager
	go2rtcAPI *go2rtcmgr.APIClient
	onCapture SnapshotFunc
}

// NewManager creates a new snapshot manager.
func NewManager(
	cfg *config.Config,
	camMgr *camera.Manager,
	go2rtcAPI *go2rtcmgr.APIClient,
	log zerolog.Logger,
) *Manager {
	return &Manager{
		log:       log,
		cfg:       cfg,
		camMgr:    camMgr,
		go2rtcAPI: go2rtcAPI,
	}
}

// OnCapture registers a callback for when a snapshot is captured.
func (m *Manager) OnCapture(fn SnapshotFunc) {
	m.onCapture = fn
}

// Run starts the snapshot scheduling loop.
func (m *Manager) Run(ctx context.Context) {
	if m.cfg.SnapshotInt <= 0 {
		m.log.Info().Msg("snapshot interval disabled")
		return
	}

	interval := time.Duration(m.cfg.SnapshotInt) * time.Second
	m.log.Info().Dur("interval", interval).Msg("snapshot manager started")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Schedule sunrise/sunset snapshots
	if m.cfg.Latitude != 0 || m.cfg.Longitude != 0 {
		go m.runSunEvents(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.captureAll(ctx)
		}
	}
}

// captureAll captures snapshots for all applicable cameras in parallel.
func (m *Manager) captureAll(ctx context.Context) {
	cameras := m.camMgr.Cameras()

	var wg sync.WaitGroup
	for _, cam := range cameras {
		if cam.GetState() != camera.StateStreaming {
			continue
		}

		name := cam.Name()

		// Check if this camera is in the snapshot list
		if len(m.cfg.SnapshotCameras) > 0 && !containsName(m.cfg.SnapshotCameras, name) {
			continue
		}

		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			m.captureOne(ctx, n)
		}(name)
	}
	wg.Wait()
}

// CaptureOne captures a snapshot for a specific camera.
func (m *Manager) CaptureOne(ctx context.Context, name string) {
	m.captureOne(ctx, name)
}

func (m *Manager) captureOne(ctx context.Context, name string) {
	m.log.Trace().Str("cam", name).Msg("requesting snapshot from go2rtc")

	jpeg, err := m.go2rtcAPI.GetSnapshot(ctx, name)
	if err != nil {
		m.log.Debug().Err(err).Str("cam", name).Msg("snapshot capture failed")
		return
	}

	// Save to disk
	if err := m.saveSnapshot(name, jpeg); err != nil {
		m.log.Warn().Err(err).Str("cam", name).Msg("snapshot save to disk failed")
	} else {
		m.log.Debug().Str("cam", name).Int("bytes", len(jpeg)).Str("dir", m.cfg.ImgDir).Msg("snapshot saved")
	}

	// Notify callback (e.g., MQTT thumbnail)
	if m.onCapture != nil {
		m.onCapture(name, jpeg)
		m.log.Trace().Str("cam", name).Msg("snapshot callback notified")
	}
}

func (m *Manager) saveSnapshot(name string, jpeg []byte) error {
	dir := m.cfg.ImgDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	filename := name + ".jpg"
	if m.cfg.SnapshotFormat != "" {
		filename = formatFilename(m.cfg.SnapshotFormat, name, time.Now())
	}

	path := filepath.Join(dir, filename)
	return os.WriteFile(path, jpeg, 0644)
}

// runSunEvents schedules snapshots at sunrise and sunset.
func (m *Manager) runSunEvents(ctx context.Context) {
	lat := m.cfg.Latitude
	lng := m.cfg.Longitude

	for {
		now := time.Now()
		nextSunrise, nextSunset := sunrise.SunriseSunset(
			lat, lng,
			now.Year(), now.Month(), now.Day(),
		)

		// If both are in the past, compute for tomorrow
		if nextSunrise.Before(now) && nextSunset.Before(now) {
			tomorrow := now.AddDate(0, 0, 1)
			nextSunrise, nextSunset = sunrise.SunriseSunset(
				lat, lng,
				tomorrow.Year(), tomorrow.Month(), tomorrow.Day(),
			)
		}

		// Find the next event
		var nextEvent time.Time
		var eventName string
		if nextSunrise.After(now) && (nextSunset.Before(now) || nextSunrise.Before(nextSunset)) {
			nextEvent = nextSunrise
			eventName = "sunrise"
		} else {
			nextEvent = nextSunset
			eventName = "sunset"
		}

		m.log.Info().
			Str("event", eventName).
			Time("at", nextEvent).
			Msg("next sun event scheduled")

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextEvent)):
			m.log.Info().Str("event", eventName).Msg("capturing sun event snapshots")
			m.captureAll(ctx)
		}
	}
}

func containsName(list []string, name string) bool {
	upper := strings.ToUpper(name)
	for _, s := range list {
		if strings.ToUpper(s) == upper {
			return true
		}
	}
	return false
}

func formatFilename(format, camName string, t time.Time) string {
	r := strings.NewReplacer(
		"{cam_name}", camName,
		"{CAM_NAME}", strings.ToUpper(camName),
		"%Y", fmt.Sprintf("%04d", t.Year()),
		"%m", fmt.Sprintf("%02d", t.Month()),
		"%d", fmt.Sprintf("%02d", t.Day()),
		"%H", fmt.Sprintf("%02d", t.Hour()),
		"%M", fmt.Sprintf("%02d", t.Minute()),
		"%S", fmt.Sprintf("%02d", t.Second()),
		"%s", fmt.Sprintf("%d", t.Unix()),
	)
	result := r.Replace(format)
	if !strings.HasSuffix(result, ".jpg") && !strings.HasSuffix(result, ".jpeg") {
		result += ".jpg"
	}
	return result
}
