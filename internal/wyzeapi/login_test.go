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

// testWyzeServer creates a comprehensive mock Wyze API that tracks call counts.
func testWyzeServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	loginCalls := &atomic.Int32{}
	refreshCalls := &atomic.Int32{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login":
			loginCalls.Add(1)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"access_token":  "access_tok_" + r.Header.Get("keyid"),
					"refresh_token": "refresh_tok",
					"user_id":       "uid_123",
				},
			})
		case "/app/user/refresh_token":
			refreshCalls.Add(1)
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
					"email":        "user@example.com",
					"nickname":     "TestUser",
					"open_user_id": "ouid_456",
					"user_code":    "UC99",
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
							"nickname":      "TestCam",
							"enr":           "test_enr",
							"firmware_ver":  "4.52.9",
							"device_params": map[string]interface{}{
								"p2p_id":            "UIDTESTCAM123456789",
								"p2p_type":          float64(4),
								"ip":                "10.0.0.50",
								"dtls":              float64(1),
								"camera_thumbnails": map[string]interface{}{},
							},
						},
					},
				},
			})
		case "/app/v2/device/set_property":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1", "data": map[string]interface{}{},
			})
		case "/app/v2/auto/run_action":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1", "data": map[string]interface{}{"result": "ok"},
			})
		case "/app/v2/device/get_device_Info":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": "1",
				"data": map[string]interface{}{
					"property_list": []interface{}{
						map[string]interface{}{"pid": "P1", "value": "1"},
					},
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
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, loginCalls, refreshCalls
}

// clientWithServer creates a Client that points at the test server.
// We achieve this by overriding the httpClient and manually calling postJSON
// with the test server URL. For full integration, we'd need to make the
// base URLs configurable. For now, we test through direct method calls.
func clientWithServer(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := NewClient(Credentials{
		Email:    "test@example.com",
		Password: "testpass",
		APIID:    "test-id",
		APIKey:   "test-key",
	}, "test-ver", zerolog.Nop())
	c.httpClient = srv.Client()
	return c
}

func TestClient_Login_Flow(t *testing.T) {
	srv, loginCalls, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)

	// Simulate login by calling postJSON directly
	headers := c.loginHeaders()
	body := map[string]interface{}{
		"email":    "test@example.com",
		"password": hashPassword("testpass"),
	}
	resp, err := c.postJSON(srv.URL+"/api/user/login", headers, body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if resp["access_token"] != "access_tok_test-id" {
		t.Errorf("access_token = %v", resp["access_token"])
	}
	if loginCalls.Load() != 1 {
		t.Errorf("login called %d times", loginCalls.Load())
	}
}

func TestClient_RefreshToken_Flow(t *testing.T) {
	srv, _, refreshCalls := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{
		AccessToken:  "old_token",
		RefreshToken: "old_refresh",
		PhoneID:      "ph123",
		ExpiresAt:    time.Now().Add(-1 * time.Hour), // expired
	}

	payload := c.authenticatedPayload("default")
	payload["refresh_token"] = c.auth.RefreshToken
	resp, err := c.postJSON(srv.URL+"/app/user/refresh_token", c.defaultHeaders(), payload)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if resp["access_token"] != "refreshed_access" {
		t.Errorf("access_token = %v", resp["access_token"])
	}
	if refreshCalls.Load() != 1 {
		t.Errorf("refresh called %d times", refreshCalls.Load())
	}
}

func TestClient_GetUserInfo_Flow(t *testing.T) {
	srv, _, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	resp, err := c.postJSON(srv.URL+"/app/user/get_user_info", c.defaultHeaders(), c.authenticatedPayload("default"))
	if err != nil {
		t.Fatalf("get_user_info: %v", err)
	}

	if resp["email"] != "user@example.com" {
		t.Errorf("email = %v", resp["email"])
	}
	if resp["nickname"] != "TestUser" {
		t.Errorf("nickname = %v", resp["nickname"])
	}
	if resp["open_user_id"] != "ouid_456" {
		t.Errorf("open_user_id = %v", resp["open_user_id"])
	}
}

func TestClient_GetCameraList_Flow(t *testing.T) {
	srv, _, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	resp, err := c.postJSON(srv.URL+"/app/v2/home_page/get_object_list", c.defaultHeaders(), c.authenticatedPayload("default"))
	if err != nil {
		t.Fatalf("get_object_list: %v", err)
	}

	dl, ok := resp["device_list"].([]interface{})
	if !ok || len(dl) == 0 {
		t.Fatal("device_list empty or missing")
	}

	dev := dl[0].(map[string]interface{})
	if dev["mac"] != "AABBCCDDEEFF" {
		t.Errorf("mac = %v", dev["mac"])
	}
}

func TestClient_SetProperty_Flow(t *testing.T) {
	srv, _, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	payload := c.authenticatedPayload("set_device_Info")
	payload["device_mac"] = "AABB"
	payload["device_model"] = "HL_CAM4"
	payload["pid"] = "P3"
	payload["pvalue"] = "1"

	_, err := c.postJSON(srv.URL+"/app/v2/device/set_property", c.defaultHeaders(), payload)
	if err != nil {
		t.Fatalf("set_property: %v", err)
	}
}

func TestClient_RunAction_Flow(t *testing.T) {
	srv, _, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	payload := c.authenticatedPayload("run_action")
	payload["action_key"] = "restart"
	payload["instance_id"] = "AABB"
	payload["provider_key"] = "HL_CAM4"
	payload["action_params"] = map[string]interface{}{}
	payload["custom_string"] = ""

	resp, err := c.postJSON(srv.URL+"/app/v2/auto/run_action", c.defaultHeaders(), payload)
	if err != nil {
		t.Fatalf("run_action: %v", err)
	}
	if resp["result"] != "ok" {
		t.Errorf("result = %v", resp["result"])
	}
}

func TestClient_GetDeviceInfo_Flow(t *testing.T) {
	srv, _, _ := testWyzeServer(t)
	c := clientWithServer(t, srv)
	c.auth = &AuthState{AccessToken: "tok", PhoneID: "ph"}

	resp, err := c.postJSON(srv.URL+"/app/v2/device/get_device_Info", c.defaultHeaders(), c.authenticatedPayload("get_device_Info"))
	if err != nil {
		t.Fatalf("get_device_info: %v", err)
	}

	pl, ok := resp["property_list"].([]interface{})
	if !ok {
		t.Fatal("missing property_list")
	}
	if len(pl) != 1 {
		t.Errorf("properties = %d", len(pl))
	}
}

func TestClient_LoginHeaders_NoAuth(t *testing.T) {
	c := NewClient(Credentials{APIKey: "k", APIID: "i"}, "v", zerolog.Nop())
	// No auth set → should generate a phone ID
	h := c.loginHeaders()
	if h["phone-id"] == "" {
		t.Error("should generate phone-id when no auth")
	}
	if h["apikey"] != "k" {
		t.Errorf("apikey = %q", h["apikey"])
	}
}

func TestClient_PostRaw(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1024)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"events": []interface{}{}},
		})
	}))
	defer srv.Close()

	c := &Client{log: zerolog.Nop(), httpClient: srv.Client()}
	_, err := c.postRaw(srv.URL+"/test", map[string]string{"content-type": "application/json"}, `{"key":"val"}`)
	if err != nil {
		t.Fatalf("postRaw: %v", err)
	}
	if gotBody != `{"key":"val"}` {
		t.Errorf("body = %q", gotBody)
	}
}
