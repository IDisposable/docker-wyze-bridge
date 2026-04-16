package wyzeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fullMockServer returns a test server that handles all Wyze API endpoints
// and a client with URLs pointed at it.
func fullMockServer(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()

	loginCount := &atomic.Int32{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			loginCount.Add(1)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token":  "fresh_access",
					"refresh_token": "fresh_refresh",
					"user_id":       "uid_001",
				},
			})
		case "/user/login": // MFA endpoint
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token":  "mfa_access",
					"refresh_token": "mfa_refresh",
					"user_id":       "uid_mfa",
				},
			})
		case "/app/user/refresh_token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token":  "refreshed_access",
					"refresh_token": "refreshed_refresh",
				},
			})
		case "/app/user/get_user_info":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"email":        "marc@test.com",
					"nickname":     "Marc",
					"open_user_id": "ouid_42",
					"user_code":    "UC1",
					"logo":         "https://logo.png",
				},
			})
		case "/app/v2/home_page/get_object_list":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"device_list": []interface{}{
						map[string]interface{}{
							"product_type":  "Camera",
							"product_model": "HL_CAM4",
							"mac":           "AABBCCDDEEFF",
							"nickname":      "Front Door",
							"enr":           "secret_enr",
							"firmware_ver":  "4.52.9.4188",
							"device_params": map[string]interface{}{
								"p2p_id":   "UIDFRONTDOOR12345678",
								"p2p_type": float64(4),
								"ip":       "192.168.1.100",
								"dtls":     float64(1),
								"camera_thumbnails": map[string]interface{}{
									"thumbnails_url": "https://thumb.jpg",
								},
							},
						},
						map[string]interface{}{
							"product_type":  "Camera",
							"product_model": "GW_GC1", // Gwell — unsupported
							"mac":           "FFEE",
							"nickname":      "OG Cam",
							"enr":           "og_enr",
							"device_params": map[string]interface{}{
								"p2p_id":   "UIDOGCAM00000000000",
								"p2p_type": float64(4),
								"ip":       "192.168.1.101",
								"camera_thumbnails": map[string]interface{}{},
							},
						},
						map[string]interface{}{
							"product_type": "Plug", // Not a camera
							"mac":          "0000",
						},
					},
				},
			})
		case "/app/v2/device/set_property":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1", "data": map[string]interface{}{},
			})
		case "/app/v2/device/get_device_Info":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"property_list": []interface{}{
						map[string]interface{}{"pid": "P1", "value": "1"},
						map[string]interface{}{"pid": "P2", "value": "1"},
						map[string]interface{}{"pid": "P3", "value": "0"},
					},
				},
			})
		case "/app/v2/auto/run_action":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{"result": "ok"},
			})
		default:
			w.WriteHeader(404)
			w.Write([]byte("unknown: " + r.URL.Path))
		}
	}))
	t.Cleanup(srv.Close)

	c := NewClient(Credentials{
		Email:    "marc@test.com",
		Password: "hunter2",
		APIID:    "test-api-id",
		APIKey:   "test-api-key",
	}, "test", zerolog.Nop())

	// Point at test server
	c.AuthURL = srv.URL
	c.WyzeURL = srv.URL + "/app"
	c.CloudURL = srv.URL + "/cloud"

	return c, loginCount
}

func TestIntegration_Login(t *testing.T) {
	c, loginCount := fullMockServer(t)

	auth, err := c.Login()
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if auth.AccessToken != "fresh_access" {
		t.Errorf("access_token = %q", auth.AccessToken)
	}
	if auth.RefreshToken != "fresh_refresh" {
		t.Errorf("refresh_token = %q", auth.RefreshToken)
	}
	if auth.UserID != "uid_001" {
		t.Errorf("user_id = %q", auth.UserID)
	}
	if auth.PhoneID == "" {
		t.Error("phone_id should be generated")
	}
	if loginCount.Load() != 1 {
		t.Errorf("login called %d times", loginCount.Load())
	}
	// Auth should be set on the client
	if c.Auth() == nil || c.Auth().AccessToken != "fresh_access" {
		t.Error("client auth not set after login")
	}
}

func TestIntegration_RefreshToken(t *testing.T) {
	c, _ := fullMockServer(t)

	// Set up existing auth that needs refresh
	c.SetAuth(&AuthState{
		AccessToken:  "old_token",
		RefreshToken: "old_refresh",
		PhoneID:      "ph123",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	auth, err := c.RefreshToken()
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if auth.AccessToken != "refreshed_access" {
		t.Errorf("access_token = %q", auth.AccessToken)
	}
	if auth.RefreshToken != "refreshed_refresh" {
		t.Errorf("refresh_token = %q", auth.RefreshToken)
	}
}

func TestIntegration_EnsureAuth_Fresh(t *testing.T) {
	c, loginCount := fullMockServer(t)

	// No auth → should login
	err := c.EnsureAuth()
	if err != nil {
		t.Fatalf("EnsureAuth: %v", err)
	}
	if loginCount.Load() != 1 {
		t.Errorf("should have logged in, count = %d", loginCount.Load())
	}
}

func TestIntegration_EnsureAuth_Valid(t *testing.T) {
	c, loginCount := fullMockServer(t)

	// Set valid auth
	c.SetAuth(&AuthState{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	})

	err := c.EnsureAuth()
	if err != nil {
		t.Fatalf("EnsureAuth: %v", err)
	}
	if loginCount.Load() != 0 {
		t.Error("should not login when auth is valid")
	}
}

func TestIntegration_EnsureAuth_NearExpiry(t *testing.T) {
	c, loginCount := fullMockServer(t)

	// Set auth that's about to expire (within 5min)
	c.SetAuth(&AuthState{
		AccessToken:  "expiring",
		RefreshToken: "ref",
		PhoneID:      "ph",
		ExpiresAt:    time.Now().Add(2 * time.Minute),
	})

	err := c.EnsureAuth()
	if err != nil {
		t.Fatalf("EnsureAuth: %v", err)
	}
	// Should have refreshed, not re-logged in
	if loginCount.Load() != 0 {
		t.Error("should refresh, not login")
	}
	if c.Auth().AccessToken != "refreshed_access" {
		t.Errorf("should have refreshed token, got %q", c.Auth().AccessToken)
	}
}

func TestIntegration_GetUserInfo(t *testing.T) {
	c, _ := fullMockServer(t)
	c.SetAuth(&AuthState{AccessToken: "tok", PhoneID: "ph", ExpiresAt: time.Now().Add(1 * time.Hour)})

	user, err := c.GetUserInfo()
	if err != nil {
		t.Fatalf("GetUserInfo: %v", err)
	}
	if user.Email != "marc@test.com" {
		t.Errorf("email = %q", user.Email)
	}
	if user.Nickname != "Marc" {
		t.Errorf("nickname = %q", user.Nickname)
	}
	if user.OpenUserID != "ouid_42" {
		t.Errorf("open_user_id = %q", user.OpenUserID)
	}
}

func TestIntegration_GetCameraList(t *testing.T) {
	c, _ := fullMockServer(t)
	c.SetAuth(&AuthState{AccessToken: "tok", PhoneID: "ph", ExpiresAt: time.Now().Add(1 * time.Hour)})

	cameras, err := c.GetCameraList()
	if err != nil {
		t.Fatalf("GetCameraList: %v", err)
	}
	// Returns 2 cameras (Front Door + OG). Gwell filtering is in camera.Manager, not here.
	// Plug is filtered because product_type != "Camera".
	if len(cameras) != 2 {
		t.Fatalf("expected 2 cameras, got %d", len(cameras))
	}
	// Find Front Door by MAC
	var cam CameraInfo
	for _, c := range cameras {
		if c.MAC == "AABBCCDDEEFF" {
			cam = c
			break
		}
	}
	if cam.Nickname != "Front Door" {
		t.Errorf("nickname = %q", cam.Nickname)
	}
	if cam.Model != "HL_CAM4" {
		t.Errorf("model = %q", cam.Model)
	}
	if cam.MAC != "AABBCCDDEEFF" {
		t.Errorf("mac = %q", cam.MAC)
	}
	if cam.LanIP != "192.168.1.100" {
		t.Errorf("ip = %q", cam.LanIP)
	}
	if !cam.DTLS {
		t.Error("dtls should be true")
	}
	if cam.P2PID != "UIDFRONTDOOR12345678" {
		t.Errorf("p2p_id = %q", cam.P2PID)
	}
	if cam.Thumbnail != "https://thumb.jpg" {
		t.Errorf("thumbnail = %q", cam.Thumbnail)
	}
	if cam.Name == "" {
		t.Error("normalized name should be generated")
	}
}

func TestIntegration_SetProperty(t *testing.T) {
	c, _ := fullMockServer(t)
	c.SetAuth(&AuthState{AccessToken: "tok", PhoneID: "ph", ExpiresAt: time.Now().Add(1 * time.Hour)})

	cam := CameraInfo{MAC: "AABBCCDDEEFF", Model: "HL_CAM4"}
	err := c.SetProperty(cam, "P3", "1")
	if err != nil {
		t.Fatalf("SetProperty: %v", err)
	}
}

func TestIntegration_GetDeviceInfo(t *testing.T) {
	c, _ := fullMockServer(t)
	c.SetAuth(&AuthState{AccessToken: "tok", PhoneID: "ph", ExpiresAt: time.Now().Add(1 * time.Hour)})

	cam := CameraInfo{MAC: "AABBCCDDEEFF", Model: "HL_CAM4"}
	props, err := c.GetDeviceInfo(cam)
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if len(props) != 3 {
		t.Errorf("expected 3 properties, got %d", len(props))
	}
}

func TestIntegration_RunAction(t *testing.T) {
	c, _ := fullMockServer(t)
	c.SetAuth(&AuthState{AccessToken: "tok", PhoneID: "ph", ExpiresAt: time.Now().Add(1 * time.Hour)})

	cam := CameraInfo{MAC: "AABBCCDDEEFF", Model: "HL_CAM4"}
	err := c.RunAction(cam, "restart")
	if err != nil {
		t.Fatalf("RunAction: %v", err)
	}
}

func TestIntegration_EnsureAuth_RefreshFails_ReLogin(t *testing.T) {
	callCount := &atomic.Int32{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			callCount.Add(1)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token": "new_token", "refresh_token": "new_ref", "user_id": "uid",
				},
			})
		case "/app/user/refresh_token":
			// Simulate refresh failure
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "2001", "msg": "token expired",
			})
		}
	}))
	defer srv.Close()

	c := NewClient(Credentials{Email: "a@b.com", Password: "p", APIID: "i", APIKey: "k"}, "v", zerolog.Nop())
	c.AuthURL = srv.URL
	c.WyzeURL = srv.URL + "/app"
	c.SetAuth(&AuthState{
		AccessToken:  "expired",
		RefreshToken: "bad_ref",
		PhoneID:      "ph",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	err := c.EnsureAuth()
	if err != nil {
		t.Fatalf("EnsureAuth: %v", err)
	}
	// Should have fallen back to re-login
	if callCount.Load() != 1 {
		t.Errorf("should have re-logged in, login count = %d", callCount.Load())
	}
	if c.Auth().AccessToken != "new_token" {
		t.Errorf("token = %q, want new_token", c.Auth().AccessToken)
	}
}
