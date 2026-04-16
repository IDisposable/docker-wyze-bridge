package camera

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// StateChangeFunc is called when a camera's state changes.
type StateChangeFunc func(cam *Camera, oldState, newState State)

// GwellProducer is the subset of *gwell.Producer the camera manager
// needs. Defined as an interface to avoid an import cycle and to let
// tests use a fake without spawning subprocesses.
type GwellProducer interface {
	// Enabled reports whether the Gwell integration is active.
	Enabled() bool
	// Connect registers the camera with the proxy and returns the
	// RTSP URL that should be handed to go2rtc.
	Connect(ctx context.Context, info wyzeapi.CameraInfo, camName, quality string, audio bool) (string, error)
	// Disconnect tears down the proxy's camera session.
	Disconnect(ctx context.Context, camName string) error
}

// Manager manages all cameras, their state machines, and integration with go2rtc.
type Manager struct {
	log        zerolog.Logger
	cfg        *config.Config
	api        *wyzeapi.Client
	go2rtc     *go2rtcmgr.APIClient
	gwell      GwellProducer // optional; nil ⇒ Gwell cameras are skipped
	filter     *Filter
	cameras    map[string]*Camera // keyed by normalized name
	mu         sync.RWMutex
	onChange   StateChangeFunc
}

// NewManager creates a new camera manager.
func NewManager(
	cfg *config.Config,
	api *wyzeapi.Client,
	go2rtcAPI *go2rtcmgr.APIClient,
	log zerolog.Logger,
) *Manager {
	return &Manager{
		log:    log,
		cfg:    cfg,
		api:    api,
		go2rtc: go2rtcAPI,
		filter: &Filter{
			Names:  cfg.FilterNames,
			Models: cfg.FilterModels,
			MACs:   cfg.FilterMACs,
			Block:  cfg.FilterBlocks,
		},
		cameras: make(map[string]*Camera),
	}
}

// SetGwellProducer attaches a Gwell producer so the manager can route
// Gwell-protocol cameras (GW_BE1/GW_GC1/GW_GC2/GW_DBD) to it instead
// of skipping them. Pass nil (or don't call it) to keep the legacy
// skip behavior.
func (m *Manager) SetGwellProducer(p GwellProducer) {
	m.gwell = p
}

// OnStateChange registers a callback for camera state changes.
func (m *Manager) OnStateChange(fn StateChangeFunc) {
	m.onChange = fn
}

// Cameras returns a snapshot of all managed cameras.
func (m *Manager) Cameras() []*Camera {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Camera, 0, len(m.cameras))
	for _, cam := range m.cameras {
		result = append(result, cam)
	}
	return result
}

// GetCamera returns a camera by name.
func (m *Manager) GetCamera(name string) *Camera {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cameras[name]
}

// InjectCamera adds a camera directly (for testing).
func (m *Manager) InjectCamera(name string, cam *Camera) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cameras[name] = cam
}

// Discover fetches cameras from the Wyze API and adds them.
func (m *Manager) Discover(ctx context.Context) error {
	cameras, err := m.api.GetCameraList()
	if err != nil {
		return err
	}

	// Filter out unsupported and user-filtered cameras.
	// Gwell-protocol cameras are only supported when a Gwell producer
	// is attached AND its integration flag is enabled; otherwise we
	// preserve the legacy "skip" behavior.
	gwellActive := m.gwell != nil && m.gwell.Enabled()
	var supported []wyzeapi.CameraInfo
	for _, cam := range cameras {
		if cam.IsGwell() && !gwellActive {
			m.log.Debug().
				Str("cam", cam.Nickname).
				Str("model", cam.Model).
				Msg("skipping Gwell camera (integration disabled)")
			continue
		}
		supported = append(supported, cam)
	}

	filtered := m.filter.Apply(supported)

	m.log.Info().
		Int("discovered", len(cameras)).
		Int("supported", len(supported)).
		Int("filtered", len(filtered)).
		Msg("camera discovery complete")

	m.mu.Lock()
	defer m.mu.Unlock()

	// Track which existing cameras are still present
	seen := make(map[string]bool)

	for _, info := range filtered {
		name := info.NormalizedName()
		seen[name] = true

		if existing, ok := m.cameras[name]; ok {
			// Update info (IP may have changed)
			existing.UpdateInfo(info)
			m.log.Debug().Str("cam", name).Str("ip", info.LanIP).Msg("camera info updated")
		} else {
			// New camera
			cam := NewCamera(
				info,
				m.cfg.CamQuality(name),
				m.cfg.CamAudio(name),
				m.cfg.CamRecord(name),
			)
			m.cameras[name] = cam
			m.log.Info().
				Str("cam", name).
				Str("model", info.ModelName()).
				Str("ip", info.LanIP).
				Msg("new camera added")
		}
	}

	// Mark cameras not in discovery as offline
	for name, cam := range m.cameras {
		if !seen[name] && cam.GetState() != StateOffline {
			m.changeState(cam, StateOffline)
			m.log.Warn().Str("cam", name).Msg("camera no longer in discovery, marking offline")
		}
	}

	return nil
}

// ConnectAll attempts to connect all offline/errored cameras to go2rtc in parallel.
func (m *Manager) ConnectAll(ctx context.Context) {
	m.mu.RLock()
	var toConnect []*Camera
	for _, c := range m.cameras {
		state := c.GetState()
		if state == StateOffline || state == StateError {
			toConnect = append(toConnect, c)
		}
	}
	m.mu.RUnlock()

	if len(toConnect) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, cam := range toConnect {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(c *Camera) {
			defer wg.Done()
			m.connectCamera(ctx, c)
		}(cam)
	}
	wg.Wait()
}

// connectCamera adds a single camera to go2rtc.
//
// For TUTK cameras this is a direct AddStream with the wyze:// URL.
// For Gwell cameras (GW_BE1/GC1/GC2/DBD) we first register the camera
// with the gwell-proxy sidecar to get a local rtsp:// URL, then hand
// THAT to go2rtc — the downstream consumers (WebRTC/HLS/recording)
// never know the difference.
func (m *Manager) connectCamera(ctx context.Context, cam *Camera) {
	m.changeState(cam, StateConnecting)

	var (
		streamURL string
		protocol  = "tutk"
	)

	if cam.Info.IsGwell() {
		if m.gwell == nil || !m.gwell.Enabled() {
			m.log.Warn().
				Str("cam", cam.Name()).
				Str("model", cam.Info.Model).
				Msg("Gwell camera discovered but Gwell integration disabled, skipping")
			m.changeState(cam, StateError)
			return
		}
		protocol = "gwell"
		url, err := m.gwell.Connect(ctx, cam.Info, cam.Name(), cam.Quality, cam.AudioOn)
		if err != nil {
			backoff := cam.IncrementError()
			m.log.Error().Err(err).
				Str("cam", cam.Name()).
				Str("ip", cam.Info.LanIP).
				Dur("backoff", backoff).
				Int("errors", cam.ErrorCount).
				Msg("failed to register gwell camera")
			return
		}
		streamURL = url
	} else {
		streamURL = cam.StreamURL()
	}

	m.log.Info().
		Str("cam", cam.Name()).
		Str("ip", cam.Info.LanIP).
		Str("model", cam.Info.ModelName()).
		Str("protocol", protocol).
		Str("quality", cam.Quality).
		Bool("audio", cam.AudioOn).
		Bool("record", cam.Record).
		Bool("dtls", cam.Info.DTLS).
		Msg("connecting camera to go2rtc")

	if err := m.go2rtc.AddStream(ctx, cam.Name(), streamURL); err != nil {
		backoff := cam.IncrementError()
		m.log.Error().Err(err).
			Str("cam", cam.Name()).
			Str("ip", cam.Info.LanIP).
			Str("protocol", protocol).
			Dur("backoff", backoff).
			Int("errors", cam.ErrorCount).
			Msg("failed to add stream to go2rtc")
		return
	}

	m.log.Info().
		Str("cam", cam.Name()).
		Str("ip", cam.Info.LanIP).
		Str("protocol", protocol).
		Msg("camera connected successfully")
	m.changeState(cam, StateStreaming)
}

// HealthCheck polls go2rtc for stream status and reconnects dead streams.
func (m *Manager) HealthCheck(ctx context.Context) {
	streams, err := m.go2rtc.ListStreams(ctx)
	if err != nil {
		m.log.Warn().Err(err).Msg("health check: failed to list streams")
		return
	}

	m.mu.RLock()
	cams := make([]*Camera, 0, len(m.cameras))
	for _, c := range m.cameras {
		cams = append(cams, c)
	}
	m.mu.RUnlock()

	for _, cam := range cams {
		if cam.GetState() != StateStreaming {
			continue
		}

		info, ok := streams[cam.Name()]
		if !ok || len(info.Producers) == 0 {
			m.log.Warn().Str("cam", cam.Name()).Msg("stream lost, reconnecting")
			m.changeState(cam, StateOffline)
		}
	}
}

// RunDiscoveryLoop runs the discovery + connect + health check loop.
func (m *Manager) RunDiscoveryLoop(ctx context.Context) {
	// Initial discovery
	if err := m.Discover(ctx); err != nil {
		m.log.Error().Err(err).Msg("initial discovery failed")
	}
	m.ConnectAll(ctx)

	refreshTicker := time.NewTicker(m.cfg.RefreshInterval)
	defer refreshTicker.Stop()

	healthTicker := time.NewTicker(30 * time.Second)
	defer healthTicker.Stop()

	reconnectTicker := time.NewTicker(10 * time.Second)
	defer reconnectTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			if err := m.Discover(ctx); err != nil {
				m.log.Error().Err(err).Msg("discovery refresh failed")
			}
			m.ConnectAll(ctx)
		case <-healthTicker.C:
			m.HealthCheck(ctx)
		case <-reconnectTicker.C:
			m.reconnectErrored(ctx)
		}
	}
}

// reconnectErrored tries to reconnect cameras in error state whose backoff has elapsed.
func (m *Manager) reconnectErrored(ctx context.Context) {
	m.mu.RLock()
	var ready []*Camera
	for _, c := range m.cameras {
		if c.GetState() == StateError {
			c.mu.RLock()
			backoff := c.BackoffDuration()
			elapsed := time.Since(c.LastSeen)
			c.mu.RUnlock()
			if elapsed >= backoff {
				ready = append(ready, c)
			}
		}
	}
	m.mu.RUnlock()

	if len(ready) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, cam := range ready {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(c *Camera) {
			defer wg.Done()
			m.connectCamera(ctx, c)
		}(cam)
	}
	wg.Wait()
}

// SetQuality changes a camera's quality and reconnects.
func (m *Manager) SetQuality(ctx context.Context, name, quality string) error {
	cam := m.GetCamera(name)
	if cam == nil {
		return nil
	}

	cam.mu.Lock()
	cam.Quality = quality
	cam.mu.Unlock()

	// Remove and re-add in go2rtc with new URL
	_ = m.go2rtc.DeleteStream(ctx, name)
	return m.go2rtc.AddStream(ctx, name, cam.StreamURL())
}

// RestartStream forces a camera reconnect.
func (m *Manager) RestartStream(ctx context.Context, name string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}

	_ = m.go2rtc.DeleteStream(ctx, name)
	m.changeState(cam, StateOffline)
	m.connectCamera(ctx, cam)
}

func (m *Manager) changeState(cam *Camera, newState State) {
	oldState := cam.GetState()
	if oldState == newState {
		return
	}
	cam.SetState(newState)
	m.log.Debug().
		Str("cam", cam.Name()).
		Str("from", oldState.String()).
		Str("to", newState.String()).
		Msg("state change")

	if m.onChange != nil {
		m.onChange(cam, oldState, newState)
	}
}
