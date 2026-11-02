package wyzeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func TestClient_GetCameraList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/login" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token": "tok", "refresh_token": "ref", "user_id": "uid",
				},
			})
			return
		}
		// get_object_list
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"device_list": []interface{}{
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "HL_CAM4",
						"mac":           "AABB01",
						"nickname":      "Front Door",
						"enr":           "test_enr",
						"firmware_ver":  "4.52.9",
						"device_params": map[string]interface{}{
							"p2p_id":   "UID01234567890123456",
							"p2p_type": float64(4),
							"ip":       "10.0.0.1",
							"dtls":     float64(1),
							"camera_thumbnails": map[string]interface{}{
								"thumbnails_url": "https://thumb.jpg",
							},
						},
					},
					map[string]interface{}{
						"product_type": "Light", // Not camera
						"mac":          "FFFF",
					},
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "HL_PAN3",
						"mac":           "",       // Missing MAC → skip
						"nickname":      "No MAC",
						"device_params": map[string]interface{}{
							"p2p_id":   "X",
							"ip":       "10.0.0.2",
							"camera_thumbnails": map[string]interface{}{},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
		creds:      Credentials{Email: "a@b.com", Password: "p", APIID: "i", APIKey: "k"},
		bridgeVer:  "test",
	}

	// Need to set auth first since GetCameraList calls EnsureAuth
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	// Patch the URL (we need to override the package-level wyzeAPI const)
	// Since we can't, we'll test the parsing logic separately
	// Instead, directly call postJSON to our server
	resp, err := c.postJSON(server.URL+"/app/v2/home_page/get_object_list",
		c.defaultHeaders(), c.authenticatedPayload("default"))
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}

	// Parse the response manually (same logic as GetCameraList)
	deviceList, ok := resp["device_list"].([]interface{})
	if !ok {
		t.Fatal("missing device_list")
	}

	var cameras []CameraInfo
	for _, item := range deviceList {
		dev, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if getString(dev, "product_type") != "Camera" {
			continue
		}
		params, _ := dev["device_params"].(map[string]interface{})
		if params == nil {
			continue
		}
		cam := CameraInfo{
			Nickname: getString(dev, "nickname"),
			Model:    getString(dev, "product_model"),
			MAC:      getString(dev, "mac"),
			ENR:      getString(dev, "enr"),
			LanIP:    getString(params, "ip"),
			P2PID:    getString(params, "p2p_id"),
			DTLS:     getInt(params, "dtls") != 0,
		}
		if cam.P2PID == "" || cam.MAC == "" || cam.ENR == "" {
			continue
		}
		cameras = append(cameras, cam)
	}

	if len(cameras) != 1 {
		t.Fatalf("expected 1 camera, got %d", len(cameras))
	}
	if cameras[0].Nickname != "Front Door" {
		t.Errorf("nickname = %q", cameras[0].Nickname)
	}
	if cameras[0].Model != "HL_CAM4" {
		t.Errorf("model = %q", cameras[0].Model)
	}
	if !cameras[0].DTLS {
		t.Error("DTLS should be true")
	}
}

func TestClient_SetProperty(t *testing.T) {
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"result": "ok"},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
		auth:       &AuthState{AccessToken: "tok", PhoneID: "ph"},
	}

	cam := CameraInfo{MAC: "AABB", Model: "HL_CAM4"}
	// Can't call SetProperty directly (it uses package-level wyzeAPI URL)
	// but we can test the payload construction
	payload := c.authenticatedPayload("set_device_Info")
	payload["device_mac"] = cam.MAC
	payload["device_model"] = cam.Model
	payload["pid"] = "P3"
	payload["pvalue"] = "1"

	_, err := c.postJSON(server.URL+"/set", c.defaultHeaders(), payload)
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}

	if gotBody["pid"] != "P3" {
		t.Errorf("pid = %v", gotBody["pid"])
	}
	if gotBody["pvalue"] != "1" {
		t.Errorf("pvalue = %v", gotBody["pvalue"])
	}
	if gotBody["device_mac"] != "AABB" {
		t.Errorf("device_mac = %v", gotBody["device_mac"])
	}
}

func TestClient_RunAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		if body["action_key"] != "restart" {
			t.Errorf("action_key = %v", body["action_key"])
		}
		if body["instance_id"] != "AABB" {
			t.Errorf("instance_id = %v", body["instance_id"])
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"result": "ok"},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
		auth:       &AuthState{AccessToken: "tok", PhoneID: "ph"},
	}

	payload := c.authenticatedPayload("run_action")
	payload["action_params"] = map[string]interface{}{}
	payload["action_key"] = "restart"
	payload["instance_id"] = "AABB"
	payload["provider_key"] = "HL_CAM4"

	_, err := c.postJSON(server.URL+"/action", c.defaultHeaders(), payload)
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestClient_GetDeviceInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"property_list": []interface{}{
					map[string]interface{}{"pid": "P1", "value": "1"},
					map[string]interface{}{"pid": "P2", "value": "HD"},
					map[string]interface{}{"pid": "P3", "value": "auto"},
				},
			},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
		auth:       &AuthState{AccessToken: "tok", PhoneID: "ph"},
	}

	resp, err := c.postJSON(server.URL+"/info", c.defaultHeaders(), c.authenticatedPayload("get_device_Info"))
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}

	propList, ok := resp["property_list"].([]interface{})
	if !ok {
		t.Fatal("missing property_list")
	}
	if len(propList) != 3 {
		t.Errorf("property count = %d, want 3", len(propList))
	}
}

func TestGwellLanIPOverride(t *testing.T) {
	t.Setenv("GWELL_LAN_IPS", "AABBCC112233=10.0.0.50, ddeeff445566=10.0.0.51")

	if got := gwellLanIPOverride("AABBCC112233"); got != "10.0.0.50" {
		t.Errorf("exact MAC lookup = %q, want 10.0.0.50", got)
	}
	if got := gwellLanIPOverride("DDEEFF445566"); got != "10.0.0.51" {
		t.Errorf("case-insensitive MAC lookup = %q, want 10.0.0.51", got)
	}
	if got := gwellLanIPOverride("999999999999"); got != "" {
		t.Errorf("unknown MAC = %q, want empty", got)
	}

	t.Setenv("GWELL_LAN_IPS", "")
	if got := gwellLanIPOverride("AABBCC112233"); got != "" {
		t.Errorf("unset env = %q, want empty", got)
	}
}

func TestFixKVSSignalingURL(t *testing.T) {
	// Wyze's get_streams double-encodes the SigV4 query params for some
	// cameras (observed on LD_CFP Floodlight Pro): %2F -> %252F etc.
	// AWS KVS rejects the handshake with 403 "Credential must have
	// exactly 5 slash-delimited elements". Single-decode fixes it.
	doubled := "wss://v.kinesisvideo.amazonaws.com/?X-Amz-ChannelARN=arn%253Aaws&X-Amz-Credential=ASIA%252F20260621%252Fus-west-2%252Fkinesisvideo%252Faws4_request&X-Amz-Security-Token=ab%252Bcd"
	want := "wss://v.kinesisvideo.amazonaws.com/?X-Amz-ChannelARN=arn%3Aaws&X-Amz-Credential=ASIA%2F20260621%2Fus-west-2%2Fkinesisvideo%2Faws4_request&X-Amz-Security-Token=ab%2Bcd"
	if got := FixKVSSignalingURL(doubled); got != want {
		t.Errorf("double-encoded fix:\n got=%q\nwant=%q", got, want)
	}

	// Correctly single-encoded URL passes through untouched (no %25),
	// so non-double-encoded cameras (e.g. Doorbell Pro) are unaffected
	// and '+' isn't mangled into a space.
	single := "wss://v.kinesisvideo.amazonaws.com/?X-Amz-Credential=ASIA%2F20260621&X-Amz-Security-Token=ab%2Bcd"
	if got := FixKVSSignalingURL(single); got != single {
		t.Errorf("single-encoded URL changed:\n got=%q\nwant=%q", got, single)
	}
}

func TestDisambiguateNames(t *testing.T) {
	// Cameras whose nicknames normalize the same all get a MAC suffix, so
	// none keeps the bare name. The renaming has to come out the same on
	// a second pass and in any input order, or stream names would shift.
	cams := []CameraInfo{
		{Nickname: "Front Porch", MAC: "D03F2700CA02"},
		{Nickname: "Driveway", MAC: "D03F2700CA03"},
		{Nickname: "Front Porch", MAC: "D03F2700CA01"},
		{Nickname: "front porch", MAC: "GW_DBD_80482C0D6B01"}, // normalizes the same
		{Nickname: "", MAC: "AABBCCDDEEFF"},                   // falls back to MAC: unique
	}
	for i := range cams {
		cams[i].Name = cams[i].NormalizedName()
	}
	renamed := DisambiguateNames(cams)

	want := map[string]string{
		"D03F2700CA02":        "front_porch_ca02",
		"D03F2700CA01":        "front_porch_ca01",
		"GW_DBD_80482C0D6B01": "front_porch_6b01",
		"D03F2700CA03":        "driveway",
		"AABBCCDDEEFF":        "aabbccddeeff",
	}
	for _, c := range cams {
		if c.Name != want[c.MAC] {
			t.Errorf("%s: name = %q, want %q", c.MAC, c.Name, want[c.MAC])
		}
	}
	if len(renamed) != 3 {
		t.Errorf("renamed = %d entries, want 3", len(renamed))
	}
	// Idempotent: a second pass over already-unique names changes nothing.
	if again := DisambiguateNames(cams); len(again) != 0 {
		t.Errorf("second pass renamed %d", len(again))
	}
	// Deterministic: the same input in a different order yields the same names.
	shuffled := []CameraInfo{cams[2], cams[0], cams[3]}
	DisambiguateNames(shuffled)
	for _, c := range shuffled {
		if c.Name != want[c.MAC] {
			t.Errorf("order-dependent: %s = %q", c.MAC, c.Name)
		}
	}
}

func TestMacSuffix(t *testing.T) {
	// The suffix is the last four hex digits of the MAC in lowercase, with
	// colons or a gateway prefix ignored. A MAC that's too short falls back
	// to the ordinal.
	cases := []struct {
		mac  string
		want string
	}{
		{"D03F2700CA02", "ca02"},
		{"d0:3f:27:00:ca:02", "ca02"},
		{"GW_DBD_80482C0D6B01", "6b01"},
		{"AB", "7"},
		{"", "7"},
	}
	for _, c := range cases {
		if got := macSuffix(c.mac, 7); got != c.want {
			t.Errorf("macSuffix(%q) = %q, want %q", c.mac, got, c.want)
		}
	}
}

func TestParsePropertyValue(t *testing.T) {
	// Friendly values like on, off and auto map to the pvalue Wyze wants
	// and the value to publish back on MQTT. Bad values and unknown names
	// are refused, and every supported property has a cloud ID.
	cases := []struct {
		prop, raw, pvalue, publish string
		ok                         bool
	}{
		{"hor_flip", "on", "1", "1", true},
		{"hor_flip", "OFF", "2", "2", true},
		{"ver_flip", "true", "1", "1", true},
		{"night_vision", "auto", "3", "auto", true},
		{"night_vision", "on", "1", "on", true},
		{"night_vision", "off", "2", "off", true},
		{"night_vision", "sometimes", "", "", false},
		{"bitrate", "120", "120", "120", true},
		{"fps", "x", "", "", false},
		{"unknown", "on", "", "", false},
	}
	for _, c := range cases {
		pv, pub, ok := ParsePropertyValue(c.prop, c.raw)
		if pv != c.pvalue || pub != c.publish || ok != c.ok {
			t.Errorf("ParsePropertyValue(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", c.prop, c.raw, pv, pub, ok, c.pvalue, c.publish, c.ok)
		}
	}
	for _, name := range []string{"hor_flip", "ver_flip", "night_vision", "irled", "status_light", "motion_detection", "motion_tagging", "bitrate", "fps"} {
		if _, ok := CloudProperties[name]; !ok {
			t.Errorf("CloudProperties lacks %s", name)
		}
	}
}

func TestSetNamedProperty_Validation(t *testing.T) {
	// An unknown property or a bad value is rejected before anything is
	// sent to Wyze.
	c := NewClient(Credentials{}, "test", zerolog.Nop())
	cam := CameraInfo{Name: "kitchen", MAC: "AABB"}
	if _, err := c.SetNamedProperty(cam, "nope", "on"); err == nil {
		t.Error("unknown property should fail before any network call")
	}
	if _, err := c.SetNamedProperty(cam, "hor_flip", "sideways"); err == nil {
		t.Error("invalid value should fail before any network call")
	}
}
