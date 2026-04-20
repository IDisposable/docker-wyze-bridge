package mqtt

import (
	"strconv"
	"strings"
)

// cloudSetProperty maps MQTT set topics to Wyze cloud SetProperty PIDs.
// Phase 1 supports properties that can be represented via SetProperty.
var cloudSetProperty = map[string]string{
	"night_vision":     "P3",
	"irled":            "P50",
	"status_light":     "P1",
	"motion_detection": "P13",
	"motion_tagging":   "P21",
	"hor_flip":         "P6",
	"ver_flip":         "P7",
	"bitrate":          "P3",
	"fps":              "P5",
}

// parseSetPropertyValue converts MQTT payloads into Wyze pvalue and
// a normalized value to publish back to MQTT.
func parseSetPropertyValue(property, raw string) (pvalue, publishValue string, ok bool) {
	v := strings.ToLower(strings.TrimSpace(raw))

	switch property {
	case "night_vision":
		switch v {
		case "auto":
			return "3", "auto", true
		case "on":
			return "1", "on", true
		case "off":
			return "2", "off", true
		}
	case "irled", "status_light", "motion_detection", "motion_tagging", "hor_flip", "ver_flip":
		switch v {
		case "on", "true", "1":
			return "1", "1", true
		case "off", "false", "2", "0":
			return "2", "2", true
		}
	case "bitrate", "fps":
		if _, err := strconv.Atoi(v); err == nil && v != "" {
			return v, v, true
		}
	}

	return "", "", false
}
