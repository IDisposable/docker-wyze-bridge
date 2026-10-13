package camera

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"

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
	log           zerolog.Logger
	cfg           *config.Config
	api           *wyzeapi.Client
	go2rtc        atomic.Pointer[go2rtcmgr.APIClient]
	filter        *Filter
	cameras       map[string]*Camera // keyed by normalized name
	mu            sync.RWMutex
	onChange      StateChangeFunc
	lakeReset     func(string)
	lakeIdleReset func(string)
	lakeHealth    map[string]lakeHealthState
	// onChronicError fires once when a camera's error count first
	// crosses chronicErrorThreshold; onChronicRecover fires when that
	// same camera reaches StateStreaming again.
	onChronicError   func(camName string, errorCount int)
	onChronicRecover func(camName string)
	chronicReported  map[string]bool
	// onProtocolFallback fires the first time a camera is auto-promoted
	// from TUTK to WebRTC after crossing the fallback threshold.
	onProtocolFallback func(camName string, oldProtocol, newProtocol string, failStreak int)
}

type lakeHealthState struct {
	bytes       int64
	lastData    time.Time
	producerIDs []int64
	required    bool
}

// chronicErrorThreshold is the consecutive-error count at which a
// camera is considered "stuck" — beyond go2rtc's transient blip into
// a sustained problem worth surfacing to operators.
const chronicErrorThreshold = 10

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
		cameras:         make(map[string]*Camera),
		chronicReported: make(map[string]bool),
		lakeHealth:      make(map[string]lakeHealthState),
	}
	if go2rtcAPI != nil {
		m.go2rtc.Store(go2rtcAPI)
	}
	return m
}

// SetLakeReset attaches the integrated gateway session cleanup before discovery.
func (m *Manager) SetLakeReset(reset func(string)) { m.lakeReset = reset }

func (m *Manager) SetLakeIdleReset(reset func(string)) { m.lakeIdleReset = reset }

func (m *Manager) lakeStreamHealthy(name string, native *go2rtcmgr.StreamInfo, now time.Time, required bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if native == nil {
		delete(m.lakeHealth, name)
		return false
	}
	if len(native.Consumers) == 0 && !required {
		delete(m.lakeHealth, name)
		return true
	}
	var bytes int64
	var producerIDs []int64
	for _, p := range native.Producers {
		bytes += p.BytesRecv
		if p.ID != 0 {
			producerIDs = append(producerIDs, p.ID)
		}
	}
	sort.Slice(producerIDs, func(i, j int) bool { return producerIDs[i] < producerIDs[j] })
	status, exists := m.lakeHealth[name]
	// go2rtc can replace an upstream producer without restarting the
	// preview encoder. After live data, rebuild the owned chain instead
	// of letting old timestamps or buffered frames survive that change.
	if required && status.required && status.bytes > 0 &&
		(bytes < status.bytes || len(status.producerIDs) > 0 && !slices.Equal(producerIDs, status.producerIDs)) {
		return false
	}
	if !exists || bytes != status.bytes || required != status.required {
		m.lakeHealth[name] = lakeHealthState{bytes: bytes, lastData: now, producerIDs: producerIDs, required: required}
		return true
	}
	return now.Sub(status.lastData) < 60*time.Second
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

// OnChronicError registers callbacks for a camera crossing the
// chronic-error threshold and for the same camera later recovering.
// Either may be nil. Each event fires at most once per crossing.
func (m *Manager) OnChronicError(onErr func(camName string, errorCount int), onRecover func(camName string)) {
	m.onChronicError = onErr
	m.onChronicRecover = onRecover
}

// OnProtocolFallback registers a callback for a camera being
// auto-promoted between streaming protocols (currently only
// TUTK → WebRTC). Fires once per camera per process lifetime.
func (m *Manager) OnProtocolFallback(fn func(camName string, oldProtocol, newProtocol string, failStreak int)) {
	m.onProtocolFallback = fn
}

// Cameras returns a snapshot of all managed cameras, sorted by name
// so callers (WebUI grid, MQTT discovery, M3U8 emit, snapshot loop)
// get stable ordering across discovery refreshes.
func (m *Manager) Cameras() []*Camera {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Camera, 0, len(m.cameras))
	for _, cam := range m.cameras {
		result = append(result, cam)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
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

	m.reapRenameOrphans(ctx, filtered)

	// Mark cameras not in discovery as offline
	for name, cam := range m.cameras {
		if !seen[name] && cam.GetState() != StateOffline {
			m.changeState(cam, StateOffline)
			m.log.Warn().Str("cam", name).Msg("camera no longer in discovery, marking offline")
		}
		if !seen[name] && cam.GetInfo().Model == "HL_PAN4" {
			m.removeLakeOutputs(ctx, name)
			// ConnectAll follows discovery; an absent Pan must not reopen
			// its cloud session simply because it is now offline.
			delete(m.cameras, name)
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

// connectCamera registers a camera's stream with go2rtc via the HTTP
// API. The stream URL is picked by protocol:
//
//   - TUTK: wyze:// source — go2rtc dials the camera directly.
//   - WebRTC (GW_BE1 / GW_DBD / any Gwell model without a LAN IP):
//     webrtc:http://loopback/internal/wyze/webrtc/<cam>#format=wyze —
//     go2rtc's native handler fetches the Wyze KVS signaling URL from
//     our shim and dials Wyze's mars-webcsrv itself.
//   - Gwell P2P (OG with LAN IP): empty URL — publish-only slot. The
//     gwell-proxy sidecar's ffmpeg RTSP PUBLISH lands on this slot.
func (m *Manager) connectCamera(ctx context.Context, cam *Camera) {
	m.changeState(cam, StateConnecting)

	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		m.log.Debug().Str("cam", cam.Name()).Msg("skipping connect — go2rtc API not yet attached")
		return
	}

	streamURL, protocol := m.streamSourceFor(cam)
	if protocol == "lake" {
		if m.cfg.Go2RTCURL != "" {
			cam.IncrementError()
			m.log.Warn().Str("cam", cam.Name()).Msg("Pan v4 streaming requires the embedded go2rtc receive window and templates")
			return
		}
		var extraYAML struct {
			Streams map[string]interface{} `yaml:"streams"`
		}
		if err := yaml.Unmarshal([]byte(m.cfg.Go2RTCExtraYAML), &extraYAML); err != nil {
			cam.IncrementError()
			m.log.Warn().Str("cam", cam.Name()).Msg("Lake outputs require valid extra go2rtc configuration")
			return
		}
		extraStreams := go2rtcmgr.ParseExtraStreams(m.cfg.Go2RTCExtraStreams)
		for _, suffix := range []string{"_lake", "_4k"} {
			alias := cam.Name() + suffix
			_, configured := extraYAML.Streams[alias]
			collision := configured || m.GetCamera(alias) != nil
			for _, extra := range extraStreams {
				collision = collision || extra.Name == alias
			}
			if collision {
				cam.IncrementError()
				m.log.Warn().Str("cam", cam.Name()).Msg("Lake output name conflicts with an existing stream")
				return
			}
		}
		// A fresh negotiation gets its own startup grace. In particular, a
		// previously stalled zero-byte session must not immediately fail
		// the replacement before it receives its first video packet.
		m.mu.Lock()
		for _, stage := range []string{"transport", "native", "preview"} {
			delete(m.lakeHealth, cam.Name()+"/"+stage)
		}
		m.mu.Unlock()
		if err := m.removeLakeOutputs(ctx, cam.Name()); err != nil {
			cam.IncrementError()
			return
		}
		raw := m.lakeTransportSource(cam.Name())
		if err := go2rtc.AddStream(ctx, cam.Name()+"_lake", raw); err != nil {
			m.removeLakeOutputs(ctx, cam.Name())
			cam.IncrementError()
			m.log.Warn().Str("cam", cam.Name()).Msg("Lake transport registration failed")
			return
		}
		native := lakeNativeSource(cam.Name())
		if err := go2rtc.AddStream(ctx, cam.Name()+"_4k", native); err != nil {
			m.removeLakeOutputs(ctx, cam.Name())
			cam.IncrementError()
			m.log.Warn().Str("cam", cam.Name()).Msg("Lake native stream registration failed")
			return
		}
	}
	snap := cam.Snapshot()

	m.log.Info().
		Str("cam", cam.Name()).
		Str("ip", snap.Info.LanIP).
		Str("model", snap.Info.ModelName()).
		Str("protocol", protocol).
		Str("quality", snap.Quality).
		Bool("audio", snap.AudioOn).
		Bool("record", snap.Record).
		Bool("dtls", snap.Info.DTLS).
		Msg("connecting camera to go2rtc")

	// Drop any prior go2rtc entry first so the PUT below is a clean
	// re-create rather than an in-place source swap. Without this,
	// a reconnect after HealthCheck saw 0 producers can leave the
	// old (dead) source pool attached and the fresh AddStream never
	// actually plays. Skipped for Gwell publish-only slots — those
	// have an active RTSP publisher we'd disrupt (and HealthCheck
	// already excludes Gwell, so we don't get here for them anyway).
	if protocol != "gwell" && protocol != "lake" {
		_ = go2rtc.DeleteStream(ctx, cam.Name())
	}

	if err := go2rtc.AddStream(ctx, cam.Name(), streamURL); err != nil {
		if protocol == "lake" {
			m.removeLakeOutputs(ctx, cam.Name())
		}
		backoff := cam.IncrementError()
		errors := cam.GetErrorCount()
		m.log.Error().Err(err).
			Str("cam", cam.Name()).
			Str("protocol", protocol).
			Dur("backoff", backoff).
			Int("errors", errors).
			Msg("failed to add stream to go2rtc")
		m.maybeReportChronic(cam.Name(), errors)
		m.recordTUTKFailure(cam, protocol)
		return
	}

	m.log.Info().
		Str("cam", cam.Name()).
		Str("protocol", protocol).
		Msg("camera connected successfully")
	m.changeState(cam, StateStreaming)
}

// streamSourceFor returns the go2rtc source URL and a label for the
// streaming protocol. Empty URL means publish-only slot (Gwell OG
// cameras receiving an RTSP PUSH from gwell-proxy).
func (m *Manager) streamSourceFor(cam *Camera) (url, protocol string) {
	info := cam.GetInfo()
	switch {
	case info.Model == "HL_PAN4":
		return lakePreviewSource(cam.Name(), cam.Snapshot().Quality), "lake"
	case cam.ForceWebRTC() || info.IsWebRTCStreamer():
		return fmt.Sprintf("webrtc:http://127.0.0.1:%d/internal/wyze/webrtc/%s#format=wyze", m.cfg.BridgePort, cam.Name()), "webrtc"
	case info.IsGwell():
		return "", "gwell"
	default:
		return cam.StreamURL(), "tutk"
	}
}

func lakePreviewSource(name, quality string) string {
	width, height := "1920", "1080"
	if quality == "sd" {
		width, height = "640", "360"
	}
	return "ffmpeg:" + name + "_4k#video=h264#width=" + width + "#height=" + height + "#input=wyze_lake_preview_input#raw=wyze_lake_preview"
}

func lakeNativeSource(name string) string {
	return "ffmpeg:" + name + "_lake#video=copy#input=wyze_lake_receive"
}

func (m *Manager) lakeTransportSource(name string) string {
	return fmt.Sprintf("webrtc:ws://127.0.0.1:%d/internal/wyze/lake-stream/%s#format=wyze-lake", m.cfg.BridgePort, name)
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
		if cam.GetInfo().IsGwell() {
			continue
		}

		info, ok := streams[cam.Name()]
		_, protocol := m.streamSourceFor(cam)
		healthy := ok && info != nil && len(info.Producers) > 0
		if protocol == "lake" {
			native := streams[cam.Name()+"_4k"]
			raw := streams[cam.Name()+"_lake"]
			if native != nil && len(native.Consumers) == 0 && info != nil && len(info.Consumers) == 0 && m.lakeIdleReset != nil {
				m.lakeIdleReset(cam.Name())
			}
			now := time.Now()
			// A preview can keep emitting stale frames after its upstream
			// workers disappear. Downstream demand must keep both upstream
			// liveness timers active, even while their readers are absent.
			demanded := info != nil && len(info.Consumers) > 0 || native != nil && len(native.Consumers) > 0
			healthy = ok && info != nil && m.lakeStreamHealthy(cam.Name()+"/transport", raw, now, demanded) && m.lakeStreamHealthy(cam.Name()+"/native", native, now, demanded)
			if healthy {
				healthy = m.lakeStreamHealthy(cam.Name()+"/preview", info, time.Now(), false)
			}
		}
		if !healthy {
			// Route through StateError with backoff so reconnectErrored's
			// 10s ticker picks it up. Marking StateOffline used to leave
			// the camera stuck until the next Discover refresh — reconnect
			// only handles StateError, not StateOffline.
			oldState := cam.GetState()
			backoff := cam.IncrementError()
			m.log.Warn().
				Str("cam", cam.Name()).
				Dur("backoff", backoff).
				Msg("stream lost, will retry")
			if m.onChange != nil && oldState != StateError {
				m.onChange(cam, oldState, StateError)
			}
			m.maybeReportChronic(cam.Name(), cam.GetErrorCount())
			// A stream that reached Streaming and then went 0-producers
			// is a TUTK-path failure signal for HL_CAM4-class regressions
			// where TUTK dial appears to succeed but the P2P session
			// never delivers frames. The current-protocol lookup is
			// per-call (streamSourceFor is idempotent).
			_, protocol := m.streamSourceFor(cam)
			m.recordTUTKFailure(cam, protocol)
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

// reapRenameOrphans removes m.cameras entries whose MAC matches a
// just-discovered camera under a different normalized name (i.e. the
// user renamed the camera in the Wyze app). Without this, the old
// entry's go2rtc stream lingers forever — Discover only ever adds
// new entries, and the old name stays "offline" while a fresh entry
// is created under the new name. Called under m.mu.Lock() by Discover.
func (m *Manager) reapRenameOrphans(ctx context.Context, filtered []wyzeapi.CameraInfo) {
	newByMAC := make(map[string]string, len(filtered))
	for _, info := range filtered {
		if info.MAC == "" {
			continue
		}
		newByMAC[info.MAC] = info.NormalizedName()
	}
	for oldName, existing := range m.cameras {
		info := existing.GetInfo()
		if info.MAC == "" {
			continue
		}
		newName, ok := newByMAC[info.MAC]
		if !ok || newName == oldName {
			continue
		}
		m.log.Info().
			Str("old_name", oldName).
			Str("new_name", newName).
			Str("mac", info.MAC).
			Msg("camera rename detected, dropping orphan entry")
		if info.Model == "HL_PAN4" {
			m.changeState(existing, StateOffline)
			m.removeLakeOutputs(ctx, oldName)
		} else if go2rtc := m.go2rtcClient(); go2rtc != nil {
			_ = go2rtc.DeleteStream(ctx, oldName)
		}
		delete(m.cameras, oldName)
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
	lake := cam.GetInfo().Model == "HL_PAN4"
	if lake && m.cfg.Go2RTCURL != "" {
		return fmt.Errorf("Pan v4 streaming requires the embedded go2rtc receive window and templates")
	}

	if !lake {
		cam.SetQuality(quality)
	}

	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		if lake {
			cam.SetQuality(quality)
		}
		return nil
	}
	// Remove and re-add in go2rtc with new URL
	if lake {
		if err := go2rtc.DeleteStreamAndStop(ctx, name, lakePreviewSource(name, "hd"), lakePreviewSource(name, "sd")); err != nil {
			return err
		}
		cam.SetQuality(quality)
		source, _ := m.streamSourceFor(cam)
		return go2rtc.AddStream(ctx, name, source)
	}
	_ = go2rtc.DeleteStream(ctx, name)
	return go2rtc.AddStream(ctx, name, cam.StreamURL())
}

// RestartStream forces a camera reconnect.
func (m *Manager) RestartStream(ctx context.Context, name string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}

	if cam.GetInfo().Model != "HL_PAN4" {
		if go2rtc := m.go2rtcClient(); go2rtc != nil {
			_ = go2rtc.DeleteStream(ctx, name)
		}
	}
	m.changeState(cam, StateOffline)
	m.connectCamera(ctx, cam)
}

// StartStream ensures a camera stream is active.
func (m *Manager) StartStream(ctx context.Context, name string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}
	if cam.GetState() == StateStreaming {
		return
	}
	m.changeState(cam, StateOffline)
	m.connectCamera(ctx, cam)
}

// StopStream removes a stream from go2rtc and marks the camera offline.
func (m *Manager) StopStream(ctx context.Context, name string) {
	cam := m.GetCamera(name)
	if cam == nil {
		return
	}
	if cam.GetInfo().Model == "HL_PAN4" {
		// Reject delayed local offers before tearing down registrations;
		// deleting a producer may itself wait for an in-flight negotiation.
		m.changeState(cam, StateOffline)
		m.removeLakeOutputs(ctx, name)
	} else if go2rtc := m.go2rtcClient(); go2rtc != nil {
		_ = go2rtc.DeleteStream(ctx, name)
	}
	m.changeState(cam, StateOffline)
}

// removeLakeOutputs closes the shared session before stopping preview, native
// and transport outputs, so teardown cannot leave detached encoders running.
// It does not acquire m.mu, so discovery can also call it while reaping a rename.
func (m *Manager) removeLakeOutputs(ctx context.Context, name string) error {
	if m.lakeReset != nil {
		m.lakeReset(name)
	}
	if m.cfg.Go2RTCURL != "" {
		m.log.Warn().Str("cam", name).Msg("Pan v4 streaming requires the embedded go2rtc receive window and templates")
		return fmt.Errorf("Pan v4 streaming requires the embedded go2rtc receive window and templates")
	}
	var firstErr error
	if api := m.go2rtcClient(); api != nil {
		outputs := []struct {
			name    string
			sources []string
		}{
			{name, []string{lakePreviewSource(name, "hd"), lakePreviewSource(name, "sd")}},
			{name + "_4k", []string{lakeNativeSource(name)}},
			{name + "_lake", []string{m.lakeTransportSource(name)}},
		}
		for _, output := range outputs {
			if err := api.DeleteStreamAndStop(ctx, output.name, output.sources...); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		m.log.Warn().Str("cam", name).Msg("Lake output cleanup failed")
	}
	return firstErr
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

	if newState == StateStreaming {
		m.clearChronic(cam.Name())
	}

	if m.onChange != nil {
		m.onChange(cam, oldState, newState)
	}
}

// maybeReportChronic fires onChronicError the first time errorCount
// crosses chronicErrorThreshold for this camera.
func (m *Manager) maybeReportChronic(camName string, errorCount int) {
	if errorCount < chronicErrorThreshold {
		return
	}
	m.mu.Lock()
	already := m.chronicReported[camName]
	if !already {
		m.chronicReported[camName] = true
	}
	m.mu.Unlock()
	if already || m.onChronicError == nil {
		return
	}
	m.onChronicError(camName, errorCount)
}

// recordTUTKFailure bumps the camera's TUTK-fail streak and, if the
// configured threshold is met, flips the camera to the WebRTC path
// for the rest of the process lifetime. Only counts when we're
// currently attempting the TUTK protocol — WebRTC / Gwell paths and
// already-promoted cameras are skipped. Wyze's 2025-02 firmware
// disabled TUTK on newer HL_CAM4 units without disabling the mars-
// webcsrv KVS path, so promotion recovers those cameras without
// operator intervention. See DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md.
func (m *Manager) recordTUTKFailure(cam *Camera, protocol string) {
	if protocol != "tutk" {
		return
	}
	threshold := m.cfg.TUTKFallbackThreshold
	if threshold <= 0 {
		return // operator disabled auto-fallback
	}
	if cam.ForceWebRTC() {
		return // already promoted
	}
	streak := cam.IncrementTUTKFail()
	if streak < threshold {
		return
	}
	// Cross-check: only promote if the model can plausibly stream
	// via WebRTC. The shim will call /v4/camera/get_streams which
	// only returns a KVS URL for cameras Wyze configured for WebRTC;
	// blindly flipping a model that doesn't have WebRTC available
	// would just swap one failure mode for another. We approximate
	// this by allowing promotion for any camera Wyze exposes MAC +
	// Model on — the shim's error response is the operator-visible
	// signal if the coin flip lands wrong.
	if !cam.SetForceWebRTC(true) {
		return
	}
	cam.ResetTUTKFail()
	m.log.Warn().
		Str("cam", cam.Name()).
		Str("model", cam.GetInfo().Model).
		Int("streak", streak).
		Msg("TUTK failing repeatedly, promoting camera to WebRTC")
	if m.onProtocolFallback != nil {
		m.onProtocolFallback(cam.Name(), "tutk", "webrtc", streak)
	}
}

// clearChronic fires onChronicRecover if the camera was previously
// reported as chronic.
func (m *Manager) clearChronic(camName string) {
	m.mu.Lock()
	had := m.chronicReported[camName]
	if had {
		delete(m.chronicReported, camName)
	}
	m.mu.Unlock()
	if !had || m.onChronicRecover == nil {
		return
	}
	m.onChronicRecover(camName)
}
