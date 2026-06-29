package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterRoutes_KnownPaths sanity-checks the route table by
// hitting each registered path through the mux and asserting we
// don't get the implicit 404. Status doesn't need to be 200 — a 401
// (auth wrap on a no-auth-configured server still passes) or 503
// (go2rtc not attached) is fine; the test is that the path resolves.
func TestRegisterRoutes_KnownPaths(t *testing.T) {
	srv, _ := testServer(t)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	cases := []struct {
		method, path string
		// notWant is the set of status codes that mean "no handler" —
		// 404 (ServeMux miss) or, for POST-required paths hit with GET,
		// our explicit 405.
		notWant []int
	}{
		{"GET", "/api/health", []int{http.StatusNotFound}},
		{"GET", "/api/version", []int{http.StatusNotFound}},
		{"GET", "/api/cameras", []int{http.StatusNotFound}},
		{"POST", "/api/discover", []int{http.StatusNotFound}},
		{"GET", "/api/metrics", []int{http.StatusNotFound}},
		{"GET", "/metrics", []int{http.StatusNotFound}},
		{"GET", "/metrics.prom", []int{http.StatusNotFound}},
		{"GET", "/dashboard.yaml", []int{http.StatusNotFound}},
		{"GET", "/cams.m3u8", []int{http.StatusNotFound}},
		{"GET", "/favicon.ico", []int{http.StatusNotFound}},
	}

	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		for _, bad := range c.notWant {
			if w.Code == bad {
				t.Errorf("%s %s returned %d (handler not registered?)", c.method, c.path, w.Code)
			}
		}
	}
}

// TestRegisterRoutes_InternalShimRequiresLoopback confirms the
// loopback guard rejects non-127.0.0.1 callers to the internal shim,
// even when the route is registered. httptest.NewRequest's default
// RemoteAddr is 192.0.2.1:1234 which falls outside the allowlist.
func TestRegisterRoutes_InternalShimRequiresLoopback(t *testing.T) {
	srv, _ := testServer(t)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/internal/wyze/Camera/CameraList", nil)
	req.RemoteAddr = "192.0.2.10:54321"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("non-loopback access returned %d, want 404", w.Code)
	}
}
