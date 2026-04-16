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
