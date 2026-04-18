package camera

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// StateChangeFunc is called when a camera's state changes.
type StateChangeFunc func(cam *Camera, oldState, newState State)

// Manager manages all cameras, their state machines, and integration with go2rtc.
//
// The go2rtc field is late-bound: main() can construct the Manager
// (and call Discover) before go2rtc is running, then inject the API
// client via SetGo2RTCAPI once go2rtc is ready. Any Manager operation
// that needs go2rtc before it's attached is a no-op that logs a warning
// rather than panicking on a nil pointer.
type Manager struct {
	log      zerolog.Logger
	cfg      *config.Config
	api      *wyzeapi.Client
	go2rtc   atomic.Pointer[go2rtcmgr.APIClient]
	filter   *Filter
	cameras  map[string]*Camera // keyed by normalized name
	mu       sync.RWMutex
	onChange StateChangeFunc
}

// NewManager creates a new camera manager. The go2rtcAPI may be nil
// at construction time — call SetGo2RTCAPI once go2rtc is ready. In
// the interim, the Manager can still do Discover() (pure Wyze API)
// but operations needing go2rtc are gated.
func NewManager(
	cfg *config.Config,
	api *wyzeapi.Client,
	go2rtcAPI *go2rtcmgr.APIClient,
	log zerolog.Logger,
) *Manager {
	m := &Manager{
		log: log,
		cfg: cfg,
		api: api,
		filter: &Filter{
			Names:  cfg.FilterNames,
			Models: cfg.FilterModels,
			MACs:   cfg.FilterMACs,
			Block:  cfg.FilterBlocks,
		},
		cameras: make(map[string]*Camera),
	}
	if go2rtcAPI != nil {
		m.go2rtc.Store(go2rtcAPI)
	}
	return m
}

// SetGo2RTCAPI attaches (or replaces) the go2rtc API client. Called by
// main() once the go2rtc subprocess is ready. Safe to call concurrently
// with ongoing Manager operations — callers that need go2rtc use
// m.go2rtcClient() and gracefully handle a nil return.
func (m *Manager) SetGo2RTCAPI(api *go2rtcmgr.APIClient) {
	m.go2rtc.Store(api)
}

// go2rtcClient returns the currently-attached go2rtc API client, or nil
// if none is attached yet. Callers must handle nil.
func (m *Manager) go2rtcClient() *go2rtcmgr.APIClient {
	return m.go2rtc.Load()
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
	// Gwell-protocol cameras only get into the registry when
	// GWELL_ENABLED=true; gwell-proxy (spawned from cmd/wyze-bridge)
	// owns their actual streaming via the /internal/wyze/* shim.
	// When the flag is off, we preserve the pre-4.0 "skip" behavior
	// so folks without GW_ cameras don't pay the subprocess cost.
	var supported []wyzeapi.CameraInfo
	for _, cam := range cameras {
		if cam.IsGwell() && !m.cfg.GwellEnabled {
			m.log.Debug().
				Str("cam", cam.Nickname).
				Str("model", cam.Model).
				Msg("skipping Gwell camera (GWELL_ENABLED=false)")
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
// Gwell cameras (GW_BE1/GC1/GC2/DBD) are handled entirely by the
// spawned gwell-proxy sidecar — its slot is pre-declared in the
// go2rtc YAML with an empty sources array at startup (see
// cmd/wyze-bridge/main.go), so there's nothing for the camera
// manager to register. We just transition state so the UI reflects
// the expected streaming posture; the actual H.264 frames will appear
// when gwell-proxy's ffmpeg RTSP PUBLISH lands on that reserved slot.
func (m *Manager) connectCamera(ctx context.Context, cam *Camera) {
	m.changeState(cam, StateConnecting)

	if cam.Info.IsGwell() {
		m.log.Info().
			Str("cam", cam.Name()).
			Str("model", cam.Info.ModelName()).
			Str("protocol", "gwell").
			Msg("Gwell camera — slot pre-declared in go2rtc YAML, awaiting gwell-proxy publish")
		m.changeState(cam, StateStreaming)
		return
	}

	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		m.log.Debug().Str("cam", cam.Name()).Msg("skipping connect — go2rtc API not yet attached")
		return
	}

	var (
		streamURL = cam.StreamURL()
		protocol  = "tutk"
	)

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

	if err := go2rtc.AddStream(ctx, cam.Name(), streamURL); err != nil {
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
	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return
	}
	streams, err := go2rtc.ListStreams(ctx)
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

		// Gwell cameras are fed by the gwell-proxy subprocess which
		// already has a deadman timer (no-data timeout) on its own
		// ffmpeg publisher. The go2rtc-side producer count is legitimately
		// zero during the 5-6 second window between our pre-registered
		// empty-src AddStream and the proxy's P2P handshake finishing.
		// Health-checking on go2rtc's Producers list would flap the
		// camera to Offline during that window, which triggers a reconnect
		// that re-PUTs the stream slot and kicks the in-flight publish —
		// the exact dance that caused gwell-proxy's ffmpeg to die with
		// av_interleaved_write_frame(): Broken pipe during bring-up.
		if cam.Info.IsGwell() {
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

	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return nil
	}
	// Remove and re-add in go2rtc with new URL
	_ = go2rtc.DeleteStream(ctx, name)
	return go2rtc.AddStream(ctx, name, cam.StreamURL())
}

// RestartStream forces a camera reconnect.
func (m *Manager) RestartStream(ctx context.Context, name string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}

	if go2rtc := m.go2rtcClient(); go2rtc != nil {
		_ = go2rtc.DeleteStream(ctx, name)
	}
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
