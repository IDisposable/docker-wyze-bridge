package wyzeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// expiredTokenServer fakes the Wyze endpoints for the 2001 recovery tests.
// get_streams and get_object_list accept only the token in validToken and
// answer code 2001 for any other. The refresh endpoint issues
// "refreshed_token" unless refreshFails is set, in which case it answers
// 2001 too. Login issues "login_token".
type expiredTokenServer struct {
	srv          *httptest.Server
	validToken   string
	refreshFails bool
	streams      atomic.Int32
	objectList   atomic.Int32
	refreshes    atomic.Int32
	logins       atomic.Int32
}

func newExpiredTokenServer(t *testing.T, validToken string) *expiredTokenServer {
	t.Helper()
	s := &expiredTokenServer{validToken: validToken}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v map[string]interface{}) {
		_ = json.NewEncoder(w).Encode(v)
	}
	expired := map[string]interface{}{"code": "2001", "msg": "access token expired"}

	mux.HandleFunc("/api/user/login", func(w http.ResponseWriter, r *http.Request) {
		s.logins.Add(1)
		write(w, map[string]interface{}{"code": "1", "data": map[string]interface{}{
			"access_token": "login_token", "refresh_token": "login_refresh", "user_id": "u",
		}})
	})
	mux.HandleFunc("/app/user/refresh_token", func(w http.ResponseWriter, r *http.Request) {
		s.refreshes.Add(1)
		if s.refreshFails {
			write(w, expired)
			return
		}
		write(w, map[string]interface{}{"code": "1", "data": map[string]interface{}{
			"access_token": "refreshed_token", "refresh_token": "refreshed_refresh",
		}})
	})
	mux.HandleFunc("/app/v4/camera/get_streams", func(w http.ResponseWriter, r *http.Request) {
		s.streams.Add(1)
		if r.Header.Get("authorization") != s.validToken {
			write(w, expired)
			return
		}
		write(w, map[string]interface{}{"code": "1", "data": []interface{}{
			map[string]interface{}{"params": map[string]interface{}{"signaling_url": "wss://example/signal"}},
		}})
	})
	mux.HandleFunc("/app/v2/home_page/get_object_list", func(w http.ResponseWriter, r *http.Request) {
		s.objectList.Add(1)
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["access_token"] != s.validToken {
			write(w, expired)
			return
		}
		write(w, map[string]interface{}{"code": "1", "data": map[string]interface{}{"device_list": []interface{}{}}})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// clientWithStaleToken returns a client holding a token that the local
// clock still considers valid for 20 hours, as after a state-file restore
// or when Wyze expires a token earlier than the assumed 23h.
func clientWithStaleToken(t *testing.T, s *expiredTokenServer) *Client {
	t.Helper()
	c := newTestClient(t, s.srv.URL)
	c.AuthURL = s.srv.URL
	c.WyzeURL = s.srv.URL + "/app"
	c.NewWyzeURL = s.srv.URL + "/app"
	c.SetAuth(&AuthState{
		AccessToken:  "stale_token",
		RefreshToken: "stale_refresh",
		PhoneID:      "phone",
		ExpiresAt:    time.Now().Add(20 * time.Hour),
	})
	return c
}

var testCam = CameraInfo{MAC: "GW_GC1_ABCDEF", Model: "GW_GC1"}

func TestGetCameraStream_2001_RefreshesAndRetries(t *testing.T) {
	s := newExpiredTokenServer(t, "refreshed_token")
	c := clientWithStaleToken(t, s)

	resp, err := c.GetCameraStream(testCam)
	if err != nil {
		t.Fatalf("GetCameraStream after 2001: %v", err)
	}
	if resp == nil {
		t.Fatal("GetCameraStream returned nil response")
	}
	if got := s.streams.Load(); got != 2 {
		t.Errorf("get_streams calls = %d, want 2 (rejected, then retried)", got)
	}
	if got := s.refreshes.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
	if got := s.logins.Load(); got != 0 {
		t.Errorf("login calls = %d, want 0 when refresh works", got)
	}
	if c.Auth().AccessToken != "refreshed_token" {
		t.Errorf("token after recovery = %q, want refreshed_token", c.Auth().AccessToken)
	}
	if !c.Auth().ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt after recovery = %v, want a future time", c.Auth().ExpiresAt)
	}
}

func TestGetCameraStream_2001_RefreshRejected_LogsIn(t *testing.T) {
	s := newExpiredTokenServer(t, "login_token")
	s.refreshFails = true
	c := clientWithStaleToken(t, s)

	if _, err := c.GetCameraStream(testCam); err != nil {
		t.Fatalf("GetCameraStream after 2001 with rejected refresh: %v", err)
	}
	if got := s.refreshes.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
	if got := s.logins.Load(); got != 1 {
		t.Errorf("login calls = %d, want 1", got)
	}
	if got := s.streams.Load(); got != 2 {
		t.Errorf("get_streams calls = %d, want 2", got)
	}
	if c.Auth().AccessToken != "login_token" {
		t.Errorf("token after recovery = %q, want login_token", c.Auth().AccessToken)
	}
}

func TestGetCameraStream_2001_RetriesOnlyOnce(t *testing.T) {
	s := newExpiredTokenServer(t, "never_valid")
	c := clientWithStaleToken(t, s)

	_, err := c.GetCameraStream(testCam)
	if err == nil {
		t.Fatal("GetCameraStream succeeded, want the 2001 error when the retry is rejected too")
	}
	if got := s.streams.Load(); got != 2 {
		t.Errorf("get_streams calls = %d, want exactly 2 (no retry loop)", got)
	}
}

func TestGetCameraList_2001_RefreshesAndRetries(t *testing.T) {
	s := newExpiredTokenServer(t, "refreshed_token")
	c := clientWithStaleToken(t, s)

	if _, err := c.GetCameraList(); err != nil {
		t.Fatalf("GetCameraList after 2001: %v", err)
	}
	if got := s.objectList.Load(); got != 2 {
		t.Errorf("get_object_list calls = %d, want 2", got)
	}
	if got := s.refreshes.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

func TestWithAuth_OtherErrorsNotRetried(t *testing.T) {
	s := newExpiredTokenServer(t, "stale_token")
	c := clientWithStaleToken(t, s)

	calls := 0
	_, err := c.withAuth(func() (map[string]interface{}, error) {
		calls++
		return nil, &WyzeAPIError{Code: "1001", Message: "bad parameter"}
	})
	if err == nil {
		t.Fatal("withAuth returned nil error, want the 1001 error")
	}
	if calls != 1 {
		t.Errorf("request calls = %d, want 1 (only 2001 is retried)", calls)
	}
	if got := s.refreshes.Load(); got != 0 {
		t.Errorf("refresh calls = %d, want 0", got)
	}
}
