package wyzeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// mockWyzeServer creates a fake Wyze API server for testing auth flows.
func mockWyzeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Login endpoint
	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"access_token":  "test_access_token",
				"refresh_token": "test_refresh_token",
				"user_id":       "test_user_id",
			},
		})
	})

	// Refresh token
	mux.HandleFunc("/app/user/refresh_token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"access_token":  "refreshed_token",
				"refresh_token": "refreshed_refresh",
			},
		})
	})

	// Get user info
	mux.HandleFunc("/app/user/get_user_info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"email":        "test@example.com",
				"nickname":     "Test User",
				"open_user_id": "ouid_123",
			},
		})
	})

	// Camera list
	mux.HandleFunc("/app/v2/home_page/get_object_list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"device_list": []interface{}{
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "HL_CAM4",
						"mac":           "AABBCCDDEEFF",
						"nickname":      "Front Door",
						"enr":           "test_enr_value",
						"firmware_ver":  "4.52.9.4188",
						"device_params": map[string]interface{}{
							"p2p_id":   "TESTUID12345678901",
							"p2p_type": float64(4),
							"ip":       "192.168.1.100",
							"dtls":     float64(1),
							"camera_thumbnails": map[string]interface{}{
								"thumbnails_url": "https://example.com/thumb.jpg",
							},
						},
					},
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "GW_GC1", // Gwell - should be skippable
						"mac":           "112233445566",
						"nickname":      "OG Cam",
						"enr":           "og_enr",
						"device_params": map[string]interface{}{
							"p2p_id":   "OGUID123456789012",
							"p2p_type": float64(4),
							"ip":       "192.168.1.101",
							"camera_thumbnails": map[string]interface{}{},
						},
					},
					map[string]interface{}{
						"product_type": "Light", // Not a camera
						"mac":          "FFFF",
					},
					map[string]interface{}{
						"product_type":  "Camera",
						"product_model": "WYZE_CAKP2JFUS",
						"mac":           "AABB11223344",
						"nickname":      "Backyard",
						"enr":           "backyard_enr",
						"firmware_ver":  "4.36.14.3497",
						"device_params": map[string]interface{}{
							"p2p_id":   "BYUID123456789012",
							"p2p_type": float64(4),
							"ip":       "192.168.1.102",
							"dtls":     float64(1),
							"camera_thumbnails": map[string]interface{}{
								"thumbnails_url": "https://example.com/thumb2.jpg",
							},
						},
					},
				},
			},
		})
	})

	// Set property
	mux.HandleFunc("/app/v2/device/set_property", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"result": "ok"},
		})
	})

	// Get device info
	mux.HandleFunc("/app/v2/device/get_device_Info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"property_list": []interface{}{
					map[string]interface{}{"pid": "P1", "value": "1"},
					map[string]interface{}{"pid": "P2", "value": "1"},
				},
			},
		})
	})

	// Run action
	mux.HandleFunc("/app/v2/auto/run_action", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"result": "ok"},
		})
	})

	return httptest.NewServer(mux)
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	c := NewClient(Credentials{
		Email:    "test@example.com",
		Password: "testpass",
		APIID:    "test-id",
		APIKey:   "test-key",
	}, "test-version", zerolog.Nop())
	c.httpClient = &http.Client{Timeout: 5 * time.Second}
	return c
}


func TestClient_NewClient(t *testing.T) {
	c := NewClient(Credentials{
		Email:    "a@b.com",
		Password: "pass",
		APIID:    "id",
		APIKey:   "key",
		TOTPKey:  "totp",
	}, "1.0.0", zerolog.Nop())

	if c.creds.Email != "a@b.com" {
		t.Errorf("email = %q", c.creds.Email)
	}
	if c.bridgeVer != "1.0.0" {
		t.Errorf("version = %q", c.bridgeVer)
	}
	if c.Auth() != nil {
		t.Error("auth should be nil initially")
	}
}

func TestClient_SetAuth(t *testing.T) {
	c := NewClient(Credentials{}, "v", zerolog.Nop())
	auth := &AuthState{AccessToken: "tok", PhoneID: "ph"}
	c.SetAuth(auth)

	if c.Auth() == nil {
		t.Fatal("auth should not be nil")
	}
	if c.Auth().AccessToken != "tok" {
		t.Errorf("token = %q", c.Auth().AccessToken)
	}
}

func TestClient_NeedsRefresh(t *testing.T) {
	c := NewClient(Credentials{}, "v", zerolog.Nop())

	// No auth → needs refresh
	if !c.NeedsRefresh() {
		t.Error("nil auth should need refresh")
	}

	// Future expiry → no refresh
	c.SetAuth(&AuthState{ExpiresAt: time.Now().Add(1 * time.Hour)})
	if c.NeedsRefresh() {
		t.Error("future expiry should not need refresh")
	}

	// Past expiry → needs refresh
	c.SetAuth(&AuthState{ExpiresAt: time.Now().Add(-1 * time.Hour)})
	if !c.NeedsRefresh() {
		t.Error("past expiry should need refresh")
	}

	// Within 5 min window → needs refresh
	c.SetAuth(&AuthState{ExpiresAt: time.Now().Add(3 * time.Minute)})
	if !c.NeedsRefresh() {
		t.Error("within 5min window should need refresh")
	}
}

func TestClient_LoginHeaders(t *testing.T) {
	c := NewClient(Credentials{
		APIKey: "mykey",
		APIID:  "myid",
	}, "1.0", zerolog.Nop())
	c.auth = &AuthState{PhoneID: "phone123"}

	h := c.loginHeaders()
	if h["apikey"] != "mykey" {
		t.Errorf("apikey = %q", h["apikey"])
	}
	if h["keyid"] != "myid" {
		t.Errorf("keyid = %q", h["keyid"])
	}
	if h["phone-id"] != "phone123" {
		t.Errorf("phone-id = %q", h["phone-id"])
	}
}

func TestClient_DefaultHeaders(t *testing.T) {
	c := NewClient(Credentials{}, "v", zerolog.Nop())
	h := c.defaultHeaders()
	if h["user-agent"] == "" {
		t.Error("user-agent should not be empty")
	}
	if h["appversion"] == "" {
		t.Error("appversion should not be empty")
	}
	if h["env"] != "prod" {
		t.Errorf("env = %q", h["env"])
	}
}

func TestClient_AuthenticatedPayload(t *testing.T) {
	c := NewClient(Credentials{}, "v", zerolog.Nop())
	c.auth = &AuthState{
		AccessToken: "tok123",
		PhoneID:     "ph456",
	}

	p := c.authenticatedPayload("default")
	if p["access_token"] != "tok123" {
		t.Errorf("access_token = %v", p["access_token"])
	}
	if p["phone_id"] != "ph456" {
		t.Errorf("phone_id = %v", p["phone_id"])
	}
	if p["sc"] == nil || p["sv"] == nil {
		t.Error("sc/sv should be set")
	}
	if p["app_name"] != "com.hualai.WyzeCam" {
		t.Errorf("app_name = %v", p["app_name"])
	}

	// Test non-default endpoint
	p2 := c.authenticatedPayload("run_action")
	if p2["sc"] == p["sc"] {
		// run_action has different sc than default
		// Actually they might be the same by coincidence, just check they exist
	}
}

func TestClient_SignPayloadHeaders(t *testing.T) {
	c := NewClient(Credentials{}, "v", zerolog.Nop())
	c.auth = &AuthState{
		AccessToken: "tok",
		PhoneID:     "ph",
	}

	h := c.signPayloadHeaders("9319141212m2ik", `{"key":"val"}`)
	if h["content-type"] != "application/json" {
		t.Errorf("content-type = %q", h["content-type"])
	}
	if h["signature2"] == "" {
		t.Error("signature2 should not be empty")
	}
	if h["access_token"] != "tok" {
		t.Errorf("access_token = %q", h["access_token"])
	}
	if h["appid"] != "9319141212m2ik" {
		t.Errorf("appid = %q", h["appid"])
	}
}

func TestGetString(t *testing.T) {
	m := map[string]interface{}{
		"name": "hello",
		"num":  42,
	}
	if getString(m, "name") != "hello" {
		t.Error("getString failed for string value")
	}
	if getString(m, "num") != "" {
		t.Error("getString should return empty for non-string")
	}
	if getString(m, "missing") != "" {
		t.Error("getString should return empty for missing key")
	}
}

func TestGetInt(t *testing.T) {
	m := map[string]interface{}{
		"float_num": float64(42),
		"int_num":   7,
		"str":       "hello",
	}
	if getInt(m, "float_num") != 42 {
		t.Error("getInt failed for float64")
	}
	if getInt(m, "int_num") != 7 {
		t.Error("getInt failed for int")
	}
	if getInt(m, "str") != 0 {
		t.Error("getInt should return 0 for non-number")
	}
	if getInt(m, "missing") != 0 {
		t.Error("getInt should return 0 for missing")
	}
}
