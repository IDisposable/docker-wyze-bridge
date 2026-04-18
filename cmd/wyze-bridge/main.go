// wyze-bridge is a WebRTC/RTSP/RTMP/HLS bridge for Wyze cameras.
// It uses go2rtc as a managed sidecar for all camera streaming.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/mqtt"
	"github.com/IDisposable/docker-wyze-bridge/internal/recording"
	"github.com/IDisposable/docker-wyze-bridge/internal/snapshot"
	"github.com/IDisposable/docker-wyze-bridge/internal/webhooks"
	"github.com/IDisposable/docker-wyze-bridge/internal/webui"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Version is set at build time via ldflags.
var Version = "4.0-beta"

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logging
	initLogging(cfg)

	log.Info().
		Str("version", Version).
		Str("log_level", cfg.LogLevel.String()).
		Msg("starting wyze-bridge")

	// Context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info().Str("signal", sig.String()).Msg("shutdown signal received")
		cancel()
	}()

	state := loadOrInitState(cfg.StateDir)

	// Initialize Wyze API client
	apiLog := log.With().Str("c", "wyzeapi").Logger()
	creds := wyzeapi.Credentials{
		Email:    cfg.WyzeEmail,
		Password: cfg.WyzePassword,
		APIID:    cfg.WyzeAPIID,
		APIKey:   cfg.WyzeAPIKey,
		TOTPKey:  cfg.WyzeTOTPKey,
	}
	apiClient := wyzeapi.NewClient(creds, Version, apiLog)

	// Restore auth from state if available
	if state.Auth != nil && state.Auth.AccessToken != "" {
		apiClient.SetAuth(state.Auth)
		log.Info().Msg("restored auth from state file")
	}

	// Construct the camera manager and WebUI server immediately
	// as this lets the WebUI come online and users see a
	// active page while we're still discovering cameras and
	// spinning up the go2rtc/gwell_proxy subprocess in
	// the background.
	camLog := log.With().Str("c", "camera").Logger()
	camMgr := camera.NewManager(cfg, apiClient, nil, camLog)

	webuiLog := log.With().Str("c", "webui").Logger()
	webServer := webui.NewServer(cfg, camMgr, nil, Version, webuiLog)
	webServer.SetMarsMinter(apiClient)

	// Start the WebUI HTTP listener ASAP. Handlers that need go2rtc
	// return 503 "bridge still starting" until we inject the API below.
	// Static pages, SSE, and the /internal/wyze shim are all ready.
	go func() {
		if err := webServer.Start(); err != nil && ctx.Err() == nil {
			log.Fatal().Err(err).Msg("WebUI server error")
		}
	}()

	go2rtcLog := log.With().Str("c", "go2rtc").Logger()
	go2rtcAPI, go2rtcMgr := setupGo2RTC(ctx, cfg, camMgr, go2rtcLog)

	// Inject the API client into camera manager and WebUI now that
	// go2rtc is reachable. Any in-flight WebUI request that was waiting
	// on this (or that got a 503 earlier) will succeed on retry.
	camMgr.SetGo2RTCAPI(go2rtcAPI)
	webServer.SetGo2RTCAPI(go2rtcAPI)

	// Spawn gwell-proxy sidecar now that the shim is listening and
	// go2rtc's RTSP server is accepting publishes into the reserved
	// Gwell slots.
	gwellProxy := startGwellProxyIfEnabled(ctx, cfg)

	mqttClient := setupMQTT(cfg, camMgr, apiClient)
	webhookClient := setupWebhooks(cfg)
	wireCameraStateChanges(ctx, cfg, camMgr, webServer, mqttClient, webhookClient, apiClient, state)

	snapLog := log.With().Str("c", "snapshot").Logger()
	snapMgr := snapshot.NewManager(cfg, camMgr, go2rtcAPI, snapLog)
	wireSnapshotHandlers(webServer, snapMgr, mqttClient)

	startBridgeHeartbeat(ctx, camMgr, webServer)

	// Snapshot pruner
	snapPruner := snapshot.NewPruner(cfg.SnapshotPath, cfg.SnapshotKeep, snapLog)

	// Recording (pruning) manager.
	recLog := log.With().Str("c", "recording").Logger()
	recMgr := recording.NewManager(cfg, recLog)

	// Start all background goroutines. The WebUI listener is already running
	go camMgr.RunDiscoveryLoop(ctx)
	go snapMgr.Run(ctx)
	go snapPruner.Run(ctx)
	go recMgr.RunPruner(ctx)

	// Wait for shutdown
	<-ctx.Done()
	log.Info().Msg("shutting down")
	shutdownBridge(webServer, mqttClient, gwellProxy, go2rtcMgr)

	// Save final state
	state.Auth = apiClient.Auth()
	if err := state.Save(cfg.StateDir); err != nil {
		log.Error().Err(err).Msg("save state on shutdown")
	}

	log.Info().Msg("goodbye")
}

func initLogging(cfg *config.Config) {
	if isatty.IsTerminal(os.Stdout.Fd()) {
		// Human-readable console output
		output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
		log.Logger = zerolog.New(output).With().Timestamp().Logger()
	} else {
		// JSON output in Docker/production
		log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
	zerolog.SetGlobalLevel(cfg.LogLevel)

	if cfg.ForceIOTCDetail {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	}
}

func loadOrInitState(stateDir string) *wyzeapi.StateFile {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		log.Fatal().Err(err).Str("dir", stateDir).Msg("cannot create state dir")
	}

	stateLog := log.With().Str("c", "state").Logger()
	state, err := wyzeapi.LoadState(stateDir, stateLog)
	if err != nil {
		log.Fatal().Err(err).Msg("load state")
	}

	return state
}

type gwellProxyHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startGwellProxyIfEnabled(ctx context.Context, cfg *config.Config) *gwellProxyHandle {
	if cfg.GwellEnabled {
		log.Info().Msg("GWELL_ENABLED=true; spawning gwell-proxy")
		proxyCtx, proxyCancel := context.WithCancel(ctx)
		handle := &gwellProxyHandle{
			cancel: proxyCancel,
			done:   make(chan struct{}),
		}
		gwellLog := log.With().Str("c", "gwell-proxy").Logger()
		go func() {
			defer close(handle.done)
			spawnGwellProxy(proxyCtx, cfg, gwellLog)
		}()
		return handle
	}

	log.Info().Msg("GWELL_ENABLED=false; GW_ cameras will be skipped")
	return nil
}

func (h *gwellProxyHandle) Stop(ctx context.Context) error {
	if h == nil {
		return nil
	}

	h.cancel()

	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func setupMQTT(cfg *config.Config, camMgr *camera.Manager, apiClient *wyzeapi.Client) *mqtt.Client {
	if !cfg.MQTTEnabled {
		return nil
	}

	mqttCfg := mqtt.Config{
		Host:           cfg.MQTTHost,
		Port:           cfg.MQTTPort,
		Username:       cfg.MQTTUsername,
		Password:       cfg.MQTTPassword,
		Topic:          cfg.MQTTTopic,
		DiscoveryTopic: cfg.MQTTDiscoveryTopic,
	}
	mqttLog := log.With().Str("c", "mqtt").Logger()
	mqttClient := mqtt.NewClient(mqttCfg, camMgr, apiClient, cfg.BridgeIP, mqttLog)

	if err := mqttClient.Connect(); err != nil {
		log.Error().Err(err).Msg("MQTT connect failed (non-fatal)")
		return nil
	}

	return mqttClient
}

func setupWebhooks(cfg *config.Config) *webhooks.Client {
	if cfg.WebhookURLs == "" {
		return nil
	}

	whLog := log.With().Str("c", "webhooks").Logger()
	webhookClient := webhooks.NewClient(webhooks.Config{
		URLs: webhooks.ParseURLs(cfg.WebhookURLs),
	}, whLog)
	log.Info().Int("urls", len(webhookClient.URLs())).Msg("webhooks configured")
	return webhookClient
}

func wireCameraStateChanges(ctx context.Context, cfg *config.Config, camMgr *camera.Manager, webServer *webui.Server, mqttClient *mqtt.Client, webhookClient *webhooks.Client, apiClient *wyzeapi.Client, state *wyzeapi.StateFile) {
	// Each notification fires in its own goroutine so none blocks the others.
	camMgr.OnStateChange(func(cam *camera.Camera, oldState, newState camera.State) {
		name := cam.Name()
		quality := cam.Quality

		go webServer.SSE().SendJSON("camera_state", map[string]interface{}{
			"name":    name,
			"state":   newState.String(),
			"quality": quality,
		})

		if mqttClient != nil && mqttClient.IsConnected() {
			go mqttClient.PublishCameraState(cam)
		}

		if webhookClient != nil && webhookClient.Enabled() {
			go func() {
				data := webhooks.FormatCameraData(
					cam.Info.LanIP, cam.Info.Model, cam.Info.FWVersion,
					cam.Info.MAC, quality,
				)
				switch newState {
				case camera.StateStreaming:
					webhookClient.SendCameraOnline(ctx, name, data)
				case camera.StateOffline:
					webhookClient.SendCameraOffline(ctx, name, data)
				case camera.StateError:
					webhookClient.SendCameraError(ctx, name, data)
				}
			}()
		}

		go func() {
			state.Auth = apiClient.Auth()
			if err := state.Save(cfg.StateDir); err != nil {
				log.Error().Err(err).Msg("save state on state change")
			}
		}()
	})
}

func wireSnapshotHandlers(webServer *webui.Server, snapMgr *snapshot.Manager, mqttClient *mqtt.Client) {
	if mqttClient != nil {
		snapMgr.OnCapture(func(camName string, jpeg []byte) {
			mqttClient.PublishThumbnail(camName, jpeg)
		})
		mqttClient.OnSnapshotRequest(func(ctx context.Context, camName string) {
			snapMgr.CaptureOne(ctx, camName)
		})
	}

	webServer.OnSnapshotRequest(func(ctx context.Context, camName string) {
		snapMgr.CaptureOne(ctx, camName)
	})
}

func startBridgeHeartbeat(ctx context.Context, camMgr *camera.Manager, webServer *webui.Server) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				webServer.SSE().SendHeartbeat()
				cams := camMgr.Cameras()
				streaming := 0
				for _, c := range cams {
					if c.GetState() == camera.StateStreaming {
						streaming++
					}
				}
				webServer.SSE().SendJSON("bridge_status", map[string]interface{}{
					"uptime":    int(time.Since(webServer.StartTime()).Seconds()),
					"streaming": streaming,
					"total":     len(cams),
				})
			}
		}
	}()
}

func shutdownBridge(webServer *webui.Server, mqttClient *mqtt.Client, gwellProxy *gwellProxyHandle, go2rtcMgr *go2rtcmgr.Manager) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := webServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutdown web server")
	}

	if mqttClient != nil {
		mqttClient.Disconnect()
	}

	if err := gwellProxy.Stop(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("stop gwell-proxy")
	}

	if go2rtcMgr != nil {
		if err := go2rtcMgr.Stop(); err != nil {
			log.Error().Err(err).Msg("stop go2rtc manager")
		}
	}
}

func setupGo2RTC(ctx context.Context, cfg *config.Config, camMgr *camera.Manager, go2rtcLog zerolog.Logger) (*go2rtcmgr.APIClient, *go2rtcmgr.Manager) {
	// Two go2rtc modes:
	if cfg.Go2RTCURL != "" {
		// External (GO2RTC_URL set) — talk to an existing instance
		// (e.g. Frigate's). Skip spawn, skip yaml write, skip
		// STREAM_AUTH (that's on their side). Recording is ignored
		// with a warning; it would write into their config which
		// we don't own. Discovery still runs so the WebUI knows the
		// camera list, but stream sources are the remote's problem.
		log.Info().Str("url", cfg.Go2RTCURL).Msg("using external go2rtc")
		perCamRecord := false
		for _, ov := range cfg.CamOverrides {
			if ov.Record != nil && *ov.Record {
				perCamRecord = true
				break
			}
		}
		if cfg.RecordAll || perCamRecord {
			log.Warn().Msg("RECORD_* settings are ignored in external go2rtc mode — configure recording in the remote go2rtc yaml")
		}
		if cfg.StreamAuth != "" {
			log.Warn().Msg("STREAM_AUTH is ignored in external go2rtc mode — configure auth in the remote go2rtc yaml")
		}

		go2rtcAPI := go2rtcmgr.NewAPIClient(cfg.Go2RTCURL, go2rtcLog)
		// Probe once to fail fast if the URL is unreachable.
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		defer probeCancel()
		if _, err := go2rtcAPI.ListStreams(probeCtx); err != nil {
			log.Fatal().Err(err).Str("url", cfg.Go2RTCURL).Msg("external go2rtc unreachable")
		}

		return go2rtcAPI, nil
	}

	// Embedded (default) — run discovery first, generate a YAML
	// with every camera's stream slot pre-declared (TUTK sources
	// get their wyze:// URL; Gwell cameras get an empty sources
	// array so go2rtc reserves the slot for gwell-proxy's RTSP
	// PUBLISH), then spawn the subprocess.

	// Run initial discovery so we can populate the YAML with every
	// camera's slot up-front. On failure we log and press on with
	// an empty streams map — the discovery loop will retry and
	// new cameras will be added via the HTTP API after go2rtc starts.
	log.Info().Msg("running initial Wyze discovery (pre-go2rtc-launch)")
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 30*time.Second)
	defer discoverCancel()
	if err := camMgr.Discover(discoverCtx); err != nil {
		log.Warn().Err(err).Msg("initial discovery failed; starting go2rtc with empty streams")
	}

	logLevel := "warn"
	if cfg.ForceIOTCDetail {
		logLevel = "debug"
	}
	configBuilder := go2rtcmgr.NewConfigBuilder(logLevel, cfg.STUNServer, cfg.BridgeIP)

	if cfg.StreamAuth != "" {
		entries := go2rtcmgr.ParseStreamAuth(cfg.StreamAuth)
		configBuilder.SetStreamAuth(entries)
		log.Info().Int("users", len(entries)).Msg("STREAM_AUTH configured")
	}

	// Pre-declare every discovered camera in the YAML:
	//   - TUTK (wyze://...): go2rtc dials immediately on startup
	//   - Gwell (empty sources []): publish-only slot; gwell-proxy's
	//     ffmpeg RTSP PUBLISH attaches into it with no codec conflict
	gwellSlots := 0
	for _, cam := range camMgr.Cameras() {
		entry := go2rtcmgr.StreamEntry{Name: cam.Name()}
		if cam.Info.IsGwell() {
			// URL "" -> YAML emits `name: []` (publish-only slot)
			gwellSlots++
		} else {
			entry.URL = cam.StreamURL()
		}
		configBuilder.AddStream(entry)
	}
	log.Info().
		Int("tutk", len(camMgr.Cameras())-gwellSlots).
		Int("gwell_slots", gwellSlots).
		Msg("go2rtc YAML streams prepared")

	go2rtcConfigPath := cfg.StateDir + "/go2rtc.yaml"
	if err := configBuilder.WriteConfig(go2rtcConfigPath); err != nil {
		log.Fatal().Err(err).Msg("write go2rtc config")
	}

	go2rtcBinary := findGo2RTCBinary()
	mgr := go2rtcmgr.NewManager(go2rtcBinary, go2rtcConfigPath, go2rtcLog)

	if err := mgr.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("start go2rtc")
	}

	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	if err := mgr.WaitReady(readyCtx, 10*time.Second); err != nil {
		log.Fatal().Err(err).Msg("go2rtc not ready")
	}

	go2rtcAPI := go2rtcmgr.NewAPIClient(mgr.APIURL(), go2rtcLog)
	return go2rtcAPI, mgr
}

func findGo2RTCBinary() string {
	// Check common locations, then PATH
	paths := []string{
		"./go2rtc",     // local dev (current dir)
		"./go2rtc.exe", // local dev (Windows)
		"/usr/local/bin/go2rtc",
		"/usr/bin/go2rtc",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "go2rtc" // fall back to PATH lookup
}
