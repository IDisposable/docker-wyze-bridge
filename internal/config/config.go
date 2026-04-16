// Package config loads and validates all bridge configuration from
// environment variables, Docker secrets, and optional YAML config files.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Config holds the canonical, validated configuration for the bridge.
type Config struct {
	// Wyze Auth
	WyzeEmail    string
	WyzePassword string
	WyzeAPIID    string
	WyzeAPIKey   string
	TOTPKey      string

	// Network
	WBIP       string
	WBPort     int
	STUNServer string

	// WebUI Auth
	WBAuth     bool
	WBUsername string
	WBPassword string
	WBAPI      string // Bearer token for REST API

	// Stream Auth
	StreamAuth string

	// MQTT
	MQTTEnabled  bool
	MQTTHost     string
	MQTTPort     int
	MQTTUsername string
	MQTTPassword string
	MQTTTopic    string
	MQTTDTopic   string // HA discovery prefix

	// Camera Filtering
	FilterNames  []string
	FilterModels []string
	FilterMACs   []string
	FilterBlocks bool

	// Camera Defaults
	Quality     string
	Audio       bool
	OfflineTime int

	// Recording
	RecordAll      bool
	RecordPath     string
	RecordFileName string
	RecordLength   time.Duration
	RecordKeep     time.Duration

	// Snapshots
	SnapshotInt     int
	SnapshotFormat  string
	SnapshotCameras []string
	SnapshotKeep    time.Duration
	ImgDir          string

	// Sunrise/Sunset
	Latitude  float64
	Longitude float64

	// Paths
	StateDir string

	// Webhooks
	WebhookURLs string // comma-separated URLs

	// Debugging
	LogLevel        zerolog.Level
	ForceIOTCDetail bool

	// Gwell (IoTVideo) P2P proxy for GW_* cameras.
	GwellEnabled     bool
	GwellBinary      string
	GwellRTSPPort    int
	GwellControlPort int
	GwellLogLevel    string

	// Per-camera overrides keyed by normalized camera name (UPPER_CASE)
	CamOverrides map[string]CamOverride

	// Refresh interval for Wyze API camera list
	RefreshInterval time.Duration
}

// CamOverride holds per-camera setting overrides.
type CamOverride struct {
	Quality *string
	Audio   *bool
	Record  *bool
}

// Load reads configuration from environment variables, Docker secrets,
// and an optional YAML config file, returning a validated Config.
func Load() (*Config, error) {
	cfg := &Config{
		// Wyze Auth
		WyzeEmail:    secret("WYZE_EMAIL"),
		WyzePassword: secret("WYZE_PASSWORD"),
		WyzeAPIID:    secretWithAlias("WYZE_API_ID", "API_ID"),
		WyzeAPIKey:   secretWithAlias("WYZE_API_KEY", "API_KEY"),
		TOTPKey:      env("TOTP_KEY", ""),

		// Network
		WBIP:       env("WB_IP", ""),
		WBPort:     envInt("WB_PORT", 5080),
		STUNServer: env("STUN_SERVER", "stun:stun.l.google.com:19302"),

		// WebUI Auth
		WBAuth:     envBool("WB_AUTH", false),
		WBUsername: env("WB_USERNAME", "wyze"),
		WBPassword: env("WB_PASSWORD", ""),
		WBAPI:      env("WB_API", ""),

		// Stream Auth
		StreamAuth: env("STREAM_AUTH", ""),

		// MQTT
		MQTTEnabled:  envBool("MQTT_ENABLED", false),
		MQTTHost:     env("MQTT_HOST", ""),
		MQTTPort:     envInt("MQTT_PORT", 1883),
		MQTTUsername: secret("MQTT_USERNAME"),
		MQTTPassword: secret("MQTT_PASSWORD"),
		MQTTTopic:    env("MQTT_TOPIC", "wyzebridge"),
		MQTTDTopic:   env("MQTT_DTOPIC", "homeassistant"),

		// Camera Filtering
		FilterNames:  envList("FILTER_NAMES"),
		FilterModels: envList("FILTER_MODELS"),
		FilterMACs:   envList("FILTER_MACS"),
		FilterBlocks: envBool("FILTER_BLOCKS", false),

		// Camera Defaults
		Quality:     env("QUALITY", "hd"),
		Audio:       envBool("AUDIO", true),
		OfflineTime: envInt("OFFLINE_TIME", 30),

		// Recording
		RecordAll:      envBool("RECORD_ALL", false),
		RecordPath:     env("RECORD_PATH", "/record/{cam_name}/%Y/%m/%d"),
		RecordFileName: env("RECORD_FILE_NAME", "%H-%M-%S"),
		RecordLength:   envDuration("RECORD_LENGTH", 60*time.Second),
		RecordKeep:     envDuration("RECORD_KEEP", 0),

		// Snapshots
		SnapshotInt:     envInt("SNAPSHOT_INT", 0),
		SnapshotFormat:  env("SNAPSHOT_FORMAT", ""),
		SnapshotCameras: envList("SNAPSHOT_CAMERAS"),
		SnapshotKeep:    envDuration("SNAPSHOT_KEEP", 0),
		ImgDir:          env("IMG_DIR", "/img"),

		// Sunrise/Sunset
		Latitude:  envFloat("LATITUDE", 0),
		Longitude: envFloat("LONGITUDE", 0),

		// Paths
		StateDir: env("STATE_DIR", "/config"),

		// Webhooks
		WebhookURLs: env("WEBHOOK_URLS", ""),

		// Debugging
		LogLevel:        parseLogLevel(env("LOG_LEVEL", "info")),
		ForceIOTCDetail: envBool("FORCE_IOTC_DETAIL", false),

		// Gwell (IoTVideo) P2P proxy. See internal/gwell and
		// DOCS/GWELL_INTEGRATION.md for details. Disabled-safe:
		// if the binary is missing the camera manager logs a
		// warning and falls back to skip-behavior.
		GwellEnabled:     envBool("GWELL_ENABLED", true),
		GwellBinary:      env("GWELL_BINARY", ""),
		GwellRTSPPort:    envInt("GWELL_RTSP_PORT", 8564),
		GwellControlPort: envInt("GWELL_CONTROL_PORT", 18564),
		GwellLogLevel:    env("GWELL_LOG_LEVEL", ""),

		// Internals
		CamOverrides:    make(map[string]CamOverride),
		RefreshInterval: envDuration("REFRESH_INTERVAL", 30*time.Minute),
	}

	// Derive default WB_PASSWORD from WYZE_EMAIL if not set
	if cfg.WBPassword == "" && cfg.WyzeEmail != "" {
		parts := strings.SplitN(cfg.WyzeEmail, "@", 2)
		cfg.WBPassword = parts[0]
	}

	// MQTT_HOST presence implies MQTT_ENABLED
	if cfg.MQTTHost != "" {
		cfg.MQTTEnabled = true
	}

	// Load optional YAML config (HA add-on)
	if err := cfg.loadYAML(); err != nil {
		// YAML is optional; log but don't fail
		fmt.Printf("warning: config.yml: %v\n", err)
	}

	// Load per-camera overrides from env
	cfg.loadCamOverrides()

	return cfg, nil
}

// CamQuality returns the effective quality for a camera.
func (c *Config) CamQuality(camName string) string {
	key := normalizeCamName(camName)
	if ov, ok := c.CamOverrides[key]; ok && ov.Quality != nil {
		return *ov.Quality
	}
	return c.Quality
}

// CamAudio returns the effective audio setting for a camera.
func (c *Config) CamAudio(camName string) bool {
	key := normalizeCamName(camName)
	if ov, ok := c.CamOverrides[key]; ok && ov.Audio != nil {
		return *ov.Audio
	}
	return c.Audio
}

// CamRecord returns the effective record setting for a camera.
func (c *Config) CamRecord(camName string) bool {
	key := normalizeCamName(camName)
	if ov, ok := c.CamOverrides[key]; ok && ov.Record != nil {
		return *ov.Record
	}
	return c.RecordAll
}

func normalizeCamName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), " ", "_"))
}

func parseLogLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}
