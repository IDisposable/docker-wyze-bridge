package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// yamlConfig represents the optional config.yml file (primarily for HA add-on users).
type yamlConfig struct {
	WyzeEmail    string `yaml:"WYZE_EMAIL"`
	WyzePassword string `yaml:"WYZE_PASSWORD"`
	WyzeAPIID    string `yaml:"WYZE_API_ID"`
	WyzeAPIKey   string `yaml:"WYZE_API_KEY"`
	TOTPKey      string `yaml:"TOTP_KEY"`

	MQTTHost     string `yaml:"MQTT_HOST"`
	MQTTPort     int    `yaml:"MQTT_PORT"`
	MQTTUsername string `yaml:"MQTT_USERNAME"`
	MQTTPassword string `yaml:"MQTT_PASSWORD"`
	MQTTEnabled  *bool  `yaml:"MQTT_ENABLED"`
	MQTTTopic    string `yaml:"MQTT_TOPIC"`
	MQTTDTopic   string `yaml:"MQTT_DTOPIC"`

	Latitude  *float64 `yaml:"LATITUDE"`
	Longitude *float64 `yaml:"LONGITUDE"`

	RecordAll bool   `yaml:"RECORD_ALL"`
	RecordDir string `yaml:"RECORD_PATH"`

	SnapshotInt int `yaml:"SNAPSHOT_INT"`

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
	setIfEmpty(&c.TOTPKey, yc.TOTPKey)

	setIfEmpty(&c.MQTTHost, yc.MQTTHost)
	setIfEmpty(&c.MQTTUsername, yc.MQTTUsername)
	setIfEmpty(&c.MQTTPassword, yc.MQTTPassword)
	setIfEmpty(&c.MQTTTopic, yc.MQTTTopic)
	setIfEmpty(&c.MQTTDTopic, yc.MQTTDTopic)
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
	setIfEmpty(&c.RecordPath, yc.RecordDir)

	if yc.SnapshotInt != 0 && c.SnapshotInt == 0 {
		c.SnapshotInt = yc.SnapshotInt
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

// loadCamOverrides scans environment for QUALITY_{CAM}, AUDIO_{CAM}, RECORD_{CAM}.
func (c *Config) loadCamOverrides() {
	for _, e := range os.Environ() {
		for _, prefix := range []string{"QUALITY_", "AUDIO_", "RECORD_"} {
			if key, val, ok := cutEnvPrefix(e, prefix); ok {
				camKey := normalizeCamName(key)
				ov := c.CamOverrides[camKey]
				switch prefix {
				case "QUALITY_":
					v := val
					ov.Quality = &v
				case "AUDIO_":
					b := parseBool(val, true)
					ov.Audio = &b
				case "RECORD_":
					b := parseBool(val, false)
					ov.Record = &b
				}
				c.CamOverrides[camKey] = ov
			}
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
