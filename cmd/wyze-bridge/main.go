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
	"github.com/IDisposable/docker-wyze-bridge/internal/gwell"
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

	// Ensure state directory exists
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		log.Fatal().Err(err).Str("dir", cfg.StateDir).Msg("cannot create state dir")
	}

	// Load persisted state
	stateLog := log.With().Str("c", "state").Logger()
	state, err := wyzeapi.LoadState(cfg.StateDir, stateLog)
	if err != nil {
		log.Fatal().Err(err).Msg("load state")
	}

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

	go2rtcLog := log.With().Str("c", "go2rtc").Logger()

	// Two go2rtc modes:
	//  1. External (GO2RTC_URL set) — talk to an existing instance
	//     (e.g. Frigate's). Skip spawn, skip yaml write, skip
	//     STREAM_AUTH (that's on their side). Recording is ignored
	//     with a warning; it would write into their config which
	//     we don't own.
	//  2. Embedded (default) — generate yaml, spawn subprocess,
	//     wait for readiness, then connect via the local API URL.
	var go2rtcAPI *go2rtcmgr.APIClient
	var mgr *go2rtcmgr.Manager
	if cfg.Go2RTCURL != "" {
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
		go2rtcAPI = go2rtcmgr.NewAPIClient(cfg.Go2RTCURL, go2rtcLog)
		// Probe once to fail fast if the URL is unreachable.
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := go2rtcAPI.ListStreams(probeCtx); err != nil {
			probeCancel()
			log.Fatal().Err(err).Str("url", cfg.Go2RTCURL).Msg("external go2rtc unreachable")
		}
		probeCancel()
	} else {
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

		go2rtcConfigPath := cfg.StateDir + "/go2rtc.yaml"
		if err := configBuilder.WriteConfig(go2rtcConfigPath); err != nil {
			log.Fatal().Err(err).Msg("write go2rtc config")
		}

		go2rtcBinary := findGo2RTCBinary()
		mgr = go2rtcmgr.NewManager(go2rtcBinary, go2rtcConfigPath, go2rtcLog)

		if err := mgr.Start(ctx); err != nil {
			log.Fatal().Err(err).Msg("start go2rtc")
		}

		readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := mgr.WaitReady(readyCtx, 10*time.Second); err != nil {
			readyCancel()
			log.Fatal().Err(err).Msg("go2rtc not ready")
		}
		readyCancel()

		go2rtcAPI = go2rtcmgr.NewAPIClient(mgr.APIURL(), go2rtcLog)
	}

	// Camera manager
	camLog := log.With().Str("c", "camera").Logger()
	camMgr := camera.NewManager(cfg, apiClient, go2rtcAPI, camLog)

	// Gwell (IoTVideo) P2P proxy for GW_* cameras.
	// Built lazily — no subprocess is spawned until the first Gwell
	// camera is connected. If GWELL_ENABLED=false, the manager is
	// never attached and Gwell cameras are skipped as before.
	if cfg.GwellEnabled {
		gwellLog := log.With().Str("c", "gwell").Logger()
		gwellCfg := gwell.Config{
			Enabled:     true,
			BinaryPath:  cfg.GwellBinary,
			RTSPPort:    cfg.GwellRTSPPort,
			ControlPort: cfg.GwellControlPort,
			StateDir:    cfg.StateDir + "/gwell",
			LogLevel:    cfg.GwellLogLevel,
		}
		if err := gwellCfg.Validate(); err != nil {
			log.Error().Err(err).Msg("invalid GWELL_* configuration; Gwell integration disabled")
		} else {
			gwellMgr := gwell.NewManager(gwellCfg, gwellLog)
			gwellProd := gwell.NewProducer(gwellMgr, apiClient, Version, gwellLog)
			camMgr.SetGwellProducer(gwellProd)
			log.Info().
				Int("rtsp_port", cfg.GwellRTSPPort).
				Int("control_port", cfg.GwellControlPort).
				Msg("Gwell producer attached (proxy will be spawned on first GW_ camera)")
		}
	} else {
		log.Info().Msg("GWELL_ENABLED=false; GW_ cameras will be skipped")
	}

	// Recording manager
	recLog := log.With().Str("c", "recording").Logger()
	recMgr := recording.NewManager(cfg, recLog)
	_ = recMgr // Used below

	// WebUI server
	webuiLog := log.With().Str("c", "webui").Logger()
	webServer := webui.NewServer(cfg, camMgr, go2rtcAPI, Version, webuiLog)

	// MQTT (optional)
	var mqttClient *mqtt.Client
	if cfg.MQTTEnabled {
		mqttLog := log.With().Str("c", "mqtt").Logger()
		mqttClient = mqtt.NewClient(mqtt.Config{
			Host:           cfg.MQTTHost,
			Port:           cfg.MQTTPort,
			Username:       cfg.MQTTUsername,
			Password:       cfg.MQTTPassword,
			Topic:          cfg.MQTTTopic,
			DiscoveryTopic: cfg.MQTTDiscoveryTopic,
		}, camMgr, apiClient, cfg.BridgeIP, mqttLog)

		if err := mqttClient.Connect(); err != nil {
			log.Error().Err(err).Msg("MQTT connect failed (non-fatal)")
			mqttClient = nil
		}
	}

	// Webhooks (optional)
	var webhookClient *webhooks.Client
	if cfg.WebhookURLs != "" {
		whLog := log.With().Str("c", "webhooks").Logger()
		webhookClient = webhooks.NewClient(webhooks.Config{
			URLs: webhooks.ParseURLs(cfg.WebhookURLs),
		}, whLog)
		log.Info().Int("urls", len(webhookClient.URLs())).Msg("webhooks configured")
	}

	// Wire camera state changes → SSE + MQTT + webhooks + state persistence
	// Each notification fires in its own goroutine so none blocks the others.
	camMgr.OnStateChange(func(cam *camera.Camera, oldState, newState camera.State) {
		name := cam.Name()
		quality := cam.Quality

		// SSE (in-process broadcast, fast but still decouple)
		go webServer.SSE().SendJSON("camera_state", map[string]interface{}{
			"name":    name,
			"state":   newState.String(),
			"quality": quality,
		})

		// MQTT
		if mqttClient != nil && mqttClient.IsConnected() {
			go mqttClient.PublishCameraState(cam)
		}

		// Webhooks
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

		// Persist state (file I/O — decouple)
		go func() {
			state.Auth = apiClient.Auth()
			state.Save(cfg.StateDir)
		}()
	})

	// Snapshot manager
	snapLog := log.With().Str("c", "snapshot").Logger()
	snapMgr := snapshot.NewManager(cfg, camMgr, go2rtcAPI, snapLog)
	if mqttClient != nil {
		snapMgr.OnCapture(func(camName string, jpeg []byte) {
			mqttClient.PublishThumbnail(camName, jpeg)
		})
		mqttClient.OnSnapshotRequest(func(ctx context.Context, camName string) {
			snapMgr.CaptureOne(ctx, camName)
		})
	}

	// Wire the WebUI's snapshot button to the same capture path MQTT uses.
	webServer.OnSnapshotRequest(func(ctx context.Context, camName string) {
		snapMgr.CaptureOne(ctx, camName)
	})

	// Snapshot pruner
	snapPruner := snapshot.NewPruner(cfg.SnapshotPath, cfg.SnapshotKeep, snapLog)

	// SSE heartbeat + bridge_status goroutine
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

	// Start all background goroutines
	go camMgr.RunDiscoveryLoop(ctx)
	go snapMgr.Run(ctx)
	go snapPruner.Run(ctx)
	go recMgr.RunPruner(ctx)

	// Start WebUI (blocks)
	go func() {
		if err := webServer.Start(); err != nil && ctx.Err() == nil {
			log.Fatal().Err(err).Msg("WebUI server error")
		}
	}()

	// Wait for shutdown
	<-ctx.Done()
	log.Info().Msg("shutting down")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	webServer.Shutdown(shutdownCtx)
	if mqttClient != nil {
		mqttClient.Disconnect()
	}
	if mgr != nil {
		mgr.Stop()
	}

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
