package wyzeapi

import (
	"fmt"
	"strconv"
	"strings"
)

// CloudProperties maps the friendly property names the bridge exposes
// (MQTT set topics, the WebUI's camera controls) to the Wyze cloud
// property ids SetProperty takes. One table so the two surfaces can't
// drift apart.
var CloudProperties = map[string]string{
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

// ParsePropertyValue converts a user-facing value ("on", "off", "auto",
// a number) into the pvalue Wyze expects and the normalized value to
// report back on the property's MQTT state topic.
func ParsePropertyValue(property, raw string) (pvalue, publishValue string, ok bool) {
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

// SetNamedProperty is SetProperty by friendly name: validates the name
// and value, sends it, and returns the normalized value to publish.
func (c *Client) SetNamedProperty(cam CameraInfo, property, raw string) (publishValue string, err error) {
	pid, ok := CloudProperties[property]
	if !ok {
		return "", fmt.Errorf("unknown property %q", property)
	}
	pvalue, publishValue, ok := ParsePropertyValue(property, raw)
	if !ok {
		return "", fmt.Errorf("invalid value %q for %s", raw, property)
	}
	if err := c.SetProperty(cam, pid, pvalue); err != nil {
		return "", err
	}
	return publishValue, nil
}
