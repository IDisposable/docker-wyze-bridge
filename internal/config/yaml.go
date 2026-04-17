package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// yamlConfig represents the optional config.yml file. Primarily useful
// for bare-Docker users who prefer a config file to env vars; HA addon
// bashios options.json into env vars before Load() runs.
//
// YAML keys mirror env var names (renamed in 4.0 — see MIGRATION.md).
type yamlConfig struct {
	WyzeEmail    string `yaml:"WYZE_EMAIL"`
	WyzePassword string `yaml:"WYZE_PASSWORD"`
	WyzeAPIID    string `yaml:"WYZE_API_ID"`
	WyzeAPIKey   string `yaml:"WYZE_API_KEY"`
	WyzeTOTPKey  string `yaml:"WYZE_TOTP_KEY"`

	MQTTHost           string `yaml:"MQTT_HOST"`
	MQTTPort           int    `yaml:"MQTT_PORT"`
	MQTTUsername       string `yaml:"MQTT_USERNAME"`
	MQTTPassword       string `yaml:"MQTT_PASSWORD"`
	MQTTEnabled        *bool  `yaml:"MQTT_ENABLED"`
	MQTTTopic          string `yaml:"MQTT_TOPIC"`
	MQTTDiscoveryTopic string `yaml:"MQTT_DISCOVERY_TOPIC"`

	Latitude  *float64 `yaml:"LATITUDE"`
	Longitude *float64 `yaml:"LONGITUDE"`

	RecordAll  bool   `yaml:"RECORD_ALL"`
	RecordPath string `yaml:"RECORD_PATH"`

	SnapshotInterval int `yaml:"SNAPSHOT_INTERVAL"`

	CamOptions []yamlCamOption `yaml:"CAM_OPTIONS"`
}

type yamlCamOption struct {
	CamName string  `yaml:"CAM_NAME"`
	Quality *string `yaml:"QUALITY"`
	Audio   *bool   `yaml:"AUDIO"`
	Record  *bool   `yaml:"RECORD"`
}

// loadYAML attempts to load config.yml from STATE_DIR.
// Env vars always take precedence; YAML fills in gaps.
func (c *Config) loadYAML() error {
	path := filepath.Join(c.StateDir, "config.yml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // not an error
	}
	if err != nil {
		return err
	}

	var yc yamlConfig
	if err := yaml.Unmarshal(data, &yc); err != nil {
		return err
	}

	// Fill in gaps (env vars already set take precedence)
	setIfEmpty(&c.WyzeEmail, yc.WyzeEmail)
	setIfEmpty(&c.WyzePassword, yc.WyzePassword)
	setIfEmpty(&c.WyzeAPIID, yc.WyzeAPIID)
	setIfEmpty(&c.WyzeAPIKey, yc.WyzeAPIKey)
	setIfEmpty(&c.WyzeTOTPKey, yc.WyzeTOTPKey)

	setIfEmpty(&c.MQTTHost, yc.MQTTHost)
	setIfEmpty(&c.MQTTUsername, yc.MQTTUsername)
	setIfEmpty(&c.MQTTPassword, yc.MQTTPassword)
	setIfEmpty(&c.MQTTTopic, yc.MQTTTopic)
	setIfEmpty(&c.MQTTDiscoveryTopic, yc.MQTTDiscoveryTopic)
	if yc.MQTTPort != 0 && c.MQTTPort == 1883 {
		c.MQTTPort = yc.MQTTPort
	}
	if yc.MQTTEnabled != nil && !c.MQTTEnabled {
		c.MQTTEnabled = *yc.MQTTEnabled
	}

	if yc.Latitude != nil && c.Latitude == 0 {
		c.Latitude = *yc.Latitude
	}
	if yc.Longitude != nil && c.Longitude == 0 {
		c.Longitude = *yc.Longitude
	}

	if yc.RecordAll && !c.RecordAll {
		c.RecordAll = true
	}
	setIfEmpty(&c.RecordPath, yc.RecordPath)

	if yc.SnapshotInterval != 0 && c.SnapshotInterval == 0 {
		c.SnapshotInterval = yc.SnapshotInterval
	}

	// Per-camera overrides from YAML
	for _, opt := range yc.CamOptions {
		key := normalizeCamName(opt.CamName)
		ov := c.CamOverrides[key]
		if opt.Quality != nil && ov.Quality == nil {
			ov.Quality = opt.Quality
		}
		if opt.Audio != nil && ov.Audio == nil {
			ov.Audio = opt.Audio
		}
		if opt.Record != nil && ov.Record == nil {
			ov.Record = opt.Record
		}
		c.CamOverrides[key] = ov
	}

	return nil
}

// loadCamOverrides scans environment for QUALITY_{CAM}, AUDIO_{CAM},
// RECORD_{CAM}, and RECORD_CMD_{CAM}. Prefix order matters: RECORD_CMD_
// is listed before RECORD_ so a longer match wins (otherwise
// RECORD_CMD_FRONT_DOOR=... would be interpreted as RECORD for camera
// "CMD_FRONT_DOOR"). Breaks out of the inner loop after the first
// matched prefix to prevent fallthrough.
func (c *Config) loadCamOverrides() {
	for _, e := range os.Environ() {
		for _, prefix := range []string{"QUALITY_", "AUDIO_", "RECORD_CMD_", "RECORD_"} {
			key, val, ok := cutEnvPrefix(e, prefix)
			if !ok {
				continue
			}
			camKey := normalizeCamName(key)
			ov := c.CamOverrides[camKey]
			switch prefix {
			case "QUALITY_":
				v := val
				ov.Quality = &v
			case "AUDIO_":
				b := parseBool(val, true)
				ov.Audio = &b
			case "RECORD_CMD_":
				v := val
				ov.RecordCmd = &v
			case "RECORD_":
				b := parseBool(val, false)
				ov.Record = &b
			}
			c.CamOverrides[camKey] = ov
			break
		}
	}
}

func setIfEmpty(dst *string, src string) {
	if *dst == "" && src != "" {
		*dst = src
	}
}

func cutEnvPrefix(envLine, prefix string) (key, val string, ok bool) {
	eqIdx := -1
	for i, ch := range envLine {
		if ch == '=' {
			eqIdx = i
			break
		}
	}
	if eqIdx < 0 {
		return "", "", false
	}
	name := envLine[:eqIdx]
	if len(name) <= len(prefix) {
		return "", "", false
	}
	upper := normalizeCamName(name)
	if len(upper) > len(prefix) && upper[:len(prefix)] == prefix {
		return upper[len(prefix):], envLine[eqIdx+1:], true
	}
	return "", "", false
}

func parseBool(s string, fallback bool) bool {
	switch s {
	case "1", "true", "yes", "on", "True", "TRUE":
		return true
	case "0", "false", "no", "off", "False", "FALSE":
		return false
	}
	return fallback
}
