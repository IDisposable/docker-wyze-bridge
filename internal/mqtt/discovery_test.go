package mqtt

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestHADevice(t *testing.T) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Nickname:  "Front Door",
		Model:     "HL_CAM4",
		MAC:       "AABBCCDDEEFF",
		FWVersion: "4.52.9.4188",
	}, "hd", true, false)

	dev := haDevice(cam)

	ids, ok := dev["identifiers"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "wyze_AABBCCDDEEFF" {
		t.Errorf("identifiers = %v", dev["identifiers"])
	}
	if dev["name"] != "Front Door" {
		t.Errorf("name = %v", dev["name"])
	}
	if dev["model"] != "HL_CAM4" {
		t.Errorf("model = %v", dev["model"])
	}
	if dev["manufacturer"] != "Wyze" {
		t.Errorf("manufacturer = %v", dev["manufacturer"])
	}
	if dev["sw_version"] != "4.52.9.4188" {
		t.Errorf("sw_version = %v", dev["sw_version"])
	}
}

func TestDiscoveryConfigJSON(t *testing.T) {
	// Test that discovery config generates valid JSON
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:      "front_door",
		Nickname:  "Front Door",
		Model:     "HL_CAM4",
		MAC:       "AABBCCDDEEFF",
		FWVersion: "4.52.9.4188",
	}, "hd", true, false)

	device := haDevice(cam)

	config := map[string]interface{}{
		"name":                  cam.Info.Nickname,
		"unique_id":             "wyze_" + cam.Info.MAC,
		"topic":                 "wyzebridge/front_door/thumbnail",
		"availability_topic":    "wyzebridge/front_door/state",
		"payload_available":     "connected",
		"payload_not_available": "disconnected",
		"device":                device,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify it round-trips
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["unique_id"] != "wyze_AABBCCDDEEFF" {
		t.Errorf("unique_id = %v", parsed["unique_id"])
	}
	if parsed["payload_available"] != "connected" {
		t.Errorf("payload_available = %v", parsed["payload_available"])
	}
}

func TestQualitySelectDiscovery(t *testing.T) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:     "backyard",
		Nickname: "Backyard",
		Model:    "WYZE_CAKP2JFUS",
		MAC:      "112233445566",
	}, "hd", true, false)

	device := haDevice(cam)

	config := map[string]interface{}{
		"name":          cam.Info.Nickname + " Quality",
		"unique_id":     "wyze_112233445566_quality",
		"state_topic":   "wyzebridge/backyard/quality",
		"command_topic": "wyzebridge/backyard/set/quality",
		"options":       []string{"hd", "sd"},
		"device":        device,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	opts, ok := parsed["options"].([]interface{})
	if !ok || len(opts) != 2 {
		t.Errorf("options = %v", parsed["options"])
	}
}

func TestAudioSwitchDiscovery(t *testing.T) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:     "garage",
		Nickname: "Garage",
		Model:    "HL_PAN3",
		MAC:      "AABB99887766",
	}, "hd", true, false)

	device := haDevice(cam)

	config := map[string]interface{}{
		"name":          "Garage Audio",
		"unique_id":     "wyze_AABB99887766_audio",
		"state_topic":   "wyzebridge/garage/audio",
		"command_topic": "wyzebridge/garage/set/audio",
		"payload_on":    "true",
		"payload_off":   "false",
		"state_on":      "true",
		"state_off":     "false",
		"device":        device,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["payload_on"] != "true" {
		t.Errorf("payload_on = %v", parsed["payload_on"])
	}
	if parsed["state_off"] != "false" {
		t.Errorf("state_off = %v", parsed["state_off"])
	}
}

// ptzPanCamera is a TUTK Pan V3 that passes the PTZ capability gate.
func ptzPanCamera() *camera.Camera {
	return camera.NewCamera(wyzeapi.CameraInfo{
		Name:      "pan_v3",
		Nickname:  "Pan V3",
		Model:     "HL_PAN3",
		MAC:       "AABBCCDDEEFF",
		FWVersion: "1.8.2.1728",
		DTLS:      true,
	}, "hd", true, false)
}

// TestHASupportsPTZ checks the discovery gate against the model registry,
// including the runtime WebRTC override, and asserts it agrees with the gate
// the PTZ command subscription uses (supportedPTZInfo refuses with the
// "TUTK Pan cameras" message before its nil-API check).
func TestHASupportsPTZ(t *testing.T) {
	cases := []struct {
		name          string
		model         string
		dtls          bool
		lanIP         string
		forceWebRTC   bool
		want          bool
	}{
		{"Pan v1", "WYZECP1_JEF", true, "192.168.1.40", false, true},
		{"Pan v2", "HL_PAN2", true, "192.168.1.41", false, true},
		{"Pan v3", "HL_PAN3", true, "192.168.1.42", false, true},
		{"Pan Pro", "HL_PANP", true, "192.168.1.43", false, true},
		{"Pan without DTLS", "HL_PAN3", false, "192.168.1.44", false, false},
		{"Pan forced to WebRTC", "HL_PAN3", true, "192.168.1.45", true, false},
		{"Cam Pan Duo", "GW_DUO", true, "192.168.1.46", false, false},
		{"OG", "GW_GC1", true, "192.168.1.47", false, false},
		{"fixed V3", "WYZE_CAKP2JFUS", true, "192.168.1.48", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cam := camera.NewCamera(wyzeapi.CameraInfo{
				Name:     "pan",
				Nickname: "Pan",
				Model:    tc.model,
				MAC:      "AABBCCDDEEFF",
				DTLS:     tc.dtls,
				LanIP:    tc.lanIP,
			}, "hd", true, false)
			if tc.forceWebRTC {
				cam.SetForceWebRTC(true)
			}

			if got := haSupportsPTZ(cam.GetInfo(), cam.ForceWebRTC()); got != tc.want {
				t.Errorf("haSupportsPTZ(%s) = %v, want %v", tc.model, got, tc.want)
			}

			// Command-side gate must agree: an unsupported camera is refused
			// with the capability message, an eligible one proceeds to the
			// (nil) Wyze API check.
			_, err := (&Client{}).supportedPTZInfo(cam)
			refused := err != nil && err.Error() == "PTZ is supported only for TUTK Pan cameras"
			if refused == tc.want {
				t.Errorf("command gate disagrees: want eligible %v, supportedPTZInfo err = %v", tc.want, err)
			}
		})
	}
}

// TestPTZDiscoveryConfigs asserts the entity surface that gets announced for
// an eligible camera: unique IDs, shared device grouping, availability, and
// that every payload_press is a payload the command handlers accept.
func TestPTZDiscoveryConfigs(t *testing.T) {
	cam := ptzPanCamera()
	info := cam.GetInfo()
	configs := ptzDiscoveryConfigs(cam.Name(), "wyzebridge", info, haDevice(cam))

	wantSpecs := 5 + 1 + 2*wyzeapi.MaxCruisePoints + 1
	if len(configs) != wantSpecs {
		t.Fatalf("configs = %d, want %d", len(configs), wantSpecs)
	}

	byID := make(map[string]haEntityConfig, len(configs))
	ptzSpecBySuffix := ptzSpecIndex()
	for _, e := range configs {
		id, _ := e.config["unique_id"].(string)
		if id != "wyze_"+info.MAC+e.suffix {
			t.Errorf("%s%s unique_id = %v, want wyze_%s%s", e.component, e.suffix, id, info.MAC, e.suffix)
		}
		if _, dup := byID[id]; dup {
			t.Errorf("duplicate unique_id %s", id)
		}
		byID[id] = e
		spec, ok := ptzSpecBySuffix[e.suffix]
		if !ok {
			t.Fatalf("%s: no spec for suffix %s", id, e.suffix)
		}
		if e.component != spec.component {
			t.Errorf("%s component = %s, want %s", id, e.component, spec.component)
		}
		if e.config["name"] != info.Nickname+" "+spec.label {
			t.Errorf("%s name = %v, want %q", id, e.config["name"], info.Nickname+" "+spec.label)
		}
		if e.config["icon"] != spec.icon {
			t.Errorf("%s icon = %v, want %s", id, e.config["icon"], spec.icon)
		}
		if e.config["qos"] != 1 {
			t.Errorf("%s qos = %v", id, e.config["qos"])
		}
		if e.config["availability_topic"] != "wyzebridge/pan_v3/state" {
			t.Errorf("%s availability_topic = %v", id, e.config["availability_topic"])
		}
		if e.config["payload_available"] != "connected" || e.config["payload_not_available"] != "disconnected" {
			t.Errorf("%s availability payloads = %v / %v", id, e.config["payload_available"], e.config["payload_not_available"])
		}
		dev, ok := e.config["device"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s device = %v", id, e.config["device"])
		}
		ids, ok := dev["identifiers"].([]string)
		if !ok || len(ids) != 1 || ids[0] != "wyze_"+info.MAC {
			t.Errorf("%s device identifiers = %v", id, dev["identifiers"])
		}
	}

	directions := map[string]string{"up": "up", "down": "down", "left": "left", "right": "right", "stop": "stop"}
	for suffix, want := range directions {
		e, ok := byID["wyze_"+info.MAC+"_ptz_"+suffix]
		if !ok {
			t.Fatalf("missing entity _ptz_%s", suffix)
		}
		if e.component != "button" {
			t.Errorf("_ptz_%s component = %s, want button", suffix, e.component)
		}
		if got := e.config["command_topic"]; got != "wyzebridge/pan_v3/set/rotary_action" {
			t.Errorf("_ptz_%s command_topic = %v", suffix, got)
		}
		payload, ok := e.config["payload_press"].(string)
		if !ok {
			t.Fatalf("_ptz_%s payload_press = %v", suffix, e.config["payload_press"])
		}
		if payload != want {
			t.Errorf("_ptz_%s payload_press = %s, want %s", suffix, payload, want)
		}
		if _, _, err := wyzeapi.ParsePTZDirection(payload); err != nil {
			t.Errorf("_ptz_%s payload %q rejected by ParsePTZDirection: %v", suffix, payload, err)
		}
	}

	save1 := byID["wyze_"+info.MAC+"_cruise_save_1"]
	if save1.component != "button" || save1.config["command_topic"] != "wyzebridge/pan_v3/set/cruise_point" {
		t.Errorf("cruise_save_1 = %+v", save1)
	}
	goto4 := byID["wyze_"+info.MAC+"_cruise_goto_4"]
	if goto4.config["payload_press"] != `{"action":"goto","slot":4}` {
		t.Errorf("cruise_goto_4 payload_press = %v", goto4.config["payload_press"])
	}
	refresh := byID["wyze_"+info.MAC+"_cruise_refresh"]
	if refresh.config["command_topic"] != "wyzebridge/pan_v3/cruise_points/get" || refresh.config["payload_press"] != "get" {
		t.Errorf("cruise_refresh = %+v", refresh.config)
	}

	// Every configured slot gets both a save and a goto button, and no
	// slot beyond wyzeapi.MaxCruisePoints.
	for slot := 1; slot <= wyzeapi.MaxCruisePoints; slot++ {
		for _, verb := range []string{"save", "goto"} {
			suffix := fmt.Sprintf("_cruise_%s_%d", verb, slot)
			if _, ok := byID["wyze_"+info.MAC+suffix]; !ok {
				t.Errorf("missing cruise entity %s", suffix)
			}
		}
	}
	if _, over := byID[fmt.Sprintf("wyze_%s_cruise_goto_%d", info.MAC, wyzeapi.MaxCruisePoints+1)]; over {
		t.Errorf("slot %d announced beyond MaxCruisePoints", wyzeapi.MaxCruisePoints+1)
	}

	for _, tc := range []struct {
		suffix   string
		wantVerb string
		wantSlot int
	}{
		{"_cruise_save_1", "save", 1},
		{"_cruise_save_4", "save", 4},
		{"_cruise_goto_1", "goto", 1},
		{"_cruise_goto_3", "goto", 3},
	} {
		e, ok := byID["wyze_"+info.MAC+tc.suffix]
		if !ok {
			t.Fatalf("missing entity %s", tc.suffix)
		}
		payload, ok := e.config["payload_press"].(string)
		if !ok {
			t.Fatalf("%s payload_press = %v", tc.suffix, e.config["payload_press"])
		}
		cmd, err := parseCruisePointCommand(payload)
		if err != nil {
			t.Errorf("%s payload %q rejected by parseCruisePointCommand: %v", tc.suffix, payload, err)
			continue
		}
		if cmd.action != tc.wantVerb || cmd.slot != tc.wantSlot {
			t.Errorf("%s payload %q parsed as %+v, want %s slot %d", tc.suffix, payload, cmd, tc.wantVerb, tc.wantSlot)
		}
	}

	sensor, ok := byID["wyze_"+info.MAC+"_cruise_points"]
	if !ok {
		t.Fatalf("missing cruise_points sensor")
	}
	if sensor.component != "sensor" {
		t.Errorf("cruise_points component = %s, want sensor", sensor.component)
	}
	if sensor.config["state_topic"] != "wyzebridge/pan_v3/cruise_points" {
		t.Errorf("cruise_points state_topic = %v", sensor.config["state_topic"])
	}
	if _, exists := sensor.config["command_topic"]; exists {
		t.Errorf("cruise_points sensor should not carry a command_topic")
	}
	if _, exists := refresh.config["state_topic"]; exists {
		t.Errorf("cruise refresh button should not carry a state_topic")
	}

	// Home Assistant consumes the discovery payload as JSON, so the raw
	// cruise-point payloads have to survive the round trip.
	data, err := json.Marshal(sensor.config)
	if err != nil {
		t.Fatalf("marshal sensor config: %v", err)
	}
	var roundTrip map[string]interface{}
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal sensor config: %v", err)
	}
	if roundTrip["unique_id"] != "wyze_"+info.MAC+"_cruise_points" {
		t.Errorf("round-tripped unique_id = %v", roundTrip["unique_id"])
	}
}

// TestPTZEntitySpecsUnique guards the announce/unannounce symmetry: the same
// table drives both, so a duplicated (component, suffix) pair would leave an
// entity behind when a camera is removed or moved off the TUTK path.
func TestPTZEntitySpecsUnique(t *testing.T) {
	seen := make(map[string]bool, len(ptzEntitySpecs))
	for _, spec := range ptzEntitySpecs {
		key := spec.component + "/" + spec.suffix
		if seen[key] {
			t.Errorf("duplicate PTZ entity %s in the discovery table", key)
		}
		seen[key] = true
		if spec.label == "" || spec.icon == "" || spec.command == "" {
			t.Errorf("%s: incomplete spec %+v", key, spec)
		}
	}
	if len(seen) != len(ptzEntitySpecs) {
		t.Errorf("specs = %d, unique = %d", len(ptzEntitySpecs), len(seen))
	}
}

// ptzSpecIndex indexes the announce table so the test can assert each emitted
// config matches the spec it came from.
func ptzSpecIndex() map[string]ptzEntitySpec {
	index := make(map[string]ptzEntitySpec, len(ptzEntitySpecs))
	for _, spec := range ptzEntitySpecs {
		index[spec.suffix] = spec
	}
	return index
}
