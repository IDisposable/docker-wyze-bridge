package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleHLSProxy_MissingCamera covers the early-return paths:
// empty path -> 400, unknown camera -> 404. The success path is
// covered by integration tests against a real go2rtc.
func TestHandleHLSProxy_MissingCamera(t *testing.T) {
	srv, _ := testServer(t)

	cases := []struct {
		path     string
		wantCode int
	}{
		{"/hls/", http.StatusBadRequest},
		{"/hls/no_such_camera.m3u8", http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		w := httptest.NewRecorder()
		srv.handleHLSProxy(w, req)
		if w.Code != c.wantCode {
			t.Errorf("GET %s = %d, want %d", c.path, w.Code, c.wantCode)
		}
	}
}

// TestHandleWSProxy_MissingSrc — same rejection logic for the WS
// upgrade endpoint. Missing src -> 400, unknown camera -> 404.
func TestHandleWSProxy_MissingSrc(t *testing.T) {
	srv, _ := testServer(t)

	cases := []struct {
		query    string
		wantCode int
	}{
		{"", http.StatusBadRequest},
		{"?src=no_such_camera", http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/ws"+c.query, nil)
		w := httptest.NewRecorder()
		srv.handleWSProxy(w, req)
		if w.Code != c.wantCode {
			t.Errorf("/ws%s = %d, want %d", c.query, w.Code, c.wantCode)
		}
	}
}
