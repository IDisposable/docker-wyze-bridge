package webui

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

const sdBody = `{"date":"20260228","hour":9,"minute":12}`

func sdServer(t *testing.T, helper string) (http.Handler, *camera.Camera) {
	t.Helper()
	cfg := &config.Config{SDDownloadHelper: helper, BridgeAuth: true,
		BridgeUsername: "tester", BridgePassword: "fixture", BridgeAPIToken: "api-fixture"}
	// Neither cloud nor go2rtc clients are available: this route must use the cache.
	mgr := camera.NewManager(cfg, nil, nil, zerolog.Nop())
	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "front_door", LanIP: "192.168.2.3",
		P2PID: "ABCDEFGHIJKLMNOPQRST", ENR: "synthetic-enr-123", MAC: "AABBCCDDEEFF",
		Model: "WYZE_CAKP2JFUS", DTLS: true}, "hd", false, false)
	mgr.InjectCamera("front_door", cam)
	s := NewServer(Options{Config: cfg, CameraMgr: mgr, Log: zerolog.Nop()})
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return s.logMiddleware(mux), cam
}

func sdRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer api-fixture")
	return r
}

func TestSDRecordingAuthenticationAndValidation(t *testing.T) {
	h, _ := sdServer(t, "")
	for _, tc := range []struct {
		name, method, path, body, auth, contentType string
		status                                      int
	}{
		{"authentication", "POST", "/api/cameras/front_door/sd/recording", sdBody, "", "application/json", 401},
		{"unknown camera", "POST", "/api/cameras/unknown/sd/recording", sdBody, "Bearer api-fixture", "application/json", 404},
		{"method", "GET", "/api/cameras/front_door/sd/recording", "", "Bearer api-fixture", "application/json", 405},
		{"content type", "POST", "/api/cameras/front_door/sd/recording", sdBody, "Bearer api-fixture", "text/plain", 415},
		{"oversized", "POST", "/api/cameras/front_door/sd/recording", strings.Repeat(" ", 1025), "Bearer api-fixture", "application/json", 413},
		{"malformed", "POST", "/api/cameras/front_door/sd/recording", `{`, "Bearer api-fixture", "application/json", 400},
		{"duplicate field", "POST", "/api/cameras/front_door/sd/recording", `{"date":"20260228","hour":9,"minute":12,"minute":13}`, "Bearer api-fixture", "application/json", 400},
		{"path injection", "POST", "/api/cameras/front_door/sd/recording", `{"date":"../../etc","hour":9,"minute":12}`, "Bearer api-fixture", "application/json", 400},
		{"disabled", "POST", "/api/cameras/front_door/sd/recording", sdBody, "Bearer api-fixture", "application/json", 501},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sdRequest(tc.method, tc.path, tc.body)
			r.Header.Set("Authorization", tc.auth)
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow")
			}
		})
	}
	r := sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody)
	r.Header.Del("Authorization")
	r.SetBasicAuth("tester", "fixture")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatalf("Basic auth did not reach disabled backend: %d", w.Code)
	}
	// A chunked body has no declared size, and still must be bounded.
	r = sdRequest("POST", "/api/cameras/front_door/sd/recording", strings.Repeat(" ", 1025))
	r.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("unbounded chunked body: %d", w.Code)
	}
}

func sdShellFixture(t *testing.T, result string) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("helper execution is Linux-only")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "scratch-path")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := `#!/bin/sh
umask 077
while [ "$#" -gt 0 ]; do
 case "$1" in
 --live) shift ;;
 --credentials) credentials="$2"; shift 2 ;;
 --download) remote="$2"; shift 2 ;;
 --media-output) media="$2"; shift 2 ;;
 --output) result="$2"; shift 2 ;;
 --max-bytes) limit="$2"; shift 2 ;;
 *) exit 2 ;;
 esac
done
[ "$limit" = 16777216 ] || exit 2
printf '%s' "$credentials" > ` + quote(marker) + `
printf 'fixture-clip' > "$media"
printf '` + result + `' "$remote" > "$result"
`
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return helper, marker
}

const sdSuccess = `{"operation":"download_sd_file","phase":"download_complete","recording_path":"%s","downloaded_bytes":12}`

func assertSDScratchRemoved(t *testing.T, marker string) {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(string(data))); !os.IsNotExist(err) {
		t.Fatalf("scratch remains: %v", err)
	}
}

func TestSDRecordingConfiguredHelperAndCleanup(t *testing.T) {
	helper, marker := sdShellFixture(t, sdSuccess)
	h, cam := sdServer(t, helper)
	before := cam.GetInfo()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	if w.Code != 200 || w.Body.String() != "fixture-clip" {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	for key, expected := range map[string]string{"Content-Type": "video/mp4", "Content-Length": "12", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Content-Disposition": "attachment; filename=20260228-0912.mp4"} {
		if w.Header().Get(key) != expected {
			t.Errorf("%s: %q", key, w.Header().Get(key))
		}
	}
	assertSDScratchRemoved(t, marker)
	if cam.GetState() != camera.StateOffline || cam.GetInfo() != before {
		t.Fatal("export changed camera state or cached metadata")
	}
}

func TestSDRecordingRejectsPairingAndHidesHelperDetails(t *testing.T) {
	helper, marker := sdShellFixture(t, `{"operation":"download_sd_file","phase":"failed","recording_path":"%s","error":"PRIVATE_PEER_DETAIL"}`)
	h, cam := sdServer(t, helper)
	info := cam.GetInfo()
	info.DTLS = false
	cam.UpdateInfo(info)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	if w.Code != 422 {
		t.Fatalf("invalid cached pairing: %d", w.Code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("helper ran with invalid pairing")
	}
	info.DTLS = true
	cam.UpdateInfo(info)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "sd_backend_incomplete") || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), info.ENR) {
		t.Fatalf("unsafe failure: %d %s", w.Code, w.Body.String())
	}
	assertSDScratchRemoved(t, marker)
}

type sdBlockingWriter struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
	fail             bool
}

func (w *sdBlockingWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestSDRecordingOwnsBusyLockThroughDeliveryAndClientFailure(t *testing.T) {
	helper, marker := sdShellFixture(t, sdSuccess)
	h, _ := sdServer(t, helper)
	w := &sdBlockingWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{}), fail: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	}()
	select {
	case <-w.started:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not start")
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	if second.Code != 409 {
		t.Errorf("concurrent export status: %d", second.Code)
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client failure did not finish")
	}
	if w.Body.Len() != 0 {
		t.Fatal("JSON was appended after media delivery failed")
	}
	assertSDScratchRemoved(t, marker)
	third := httptest.NewRecorder()
	h.ServeHTTP(third, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
	if third.Code != 200 {
		t.Fatalf("lock not released after failed copy: %d", third.Code)
	}
}

type sdDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	err      error
}

func (w *sdDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return w.err
}

func TestSDRecordingDeadlineThroughLoggingWrapper(t *testing.T) {
	h, _ := sdServer(t, "")
	for _, fail := range []bool{false, true} {
		w := &sdDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		if fail {
			w.err = errors.New("PRIVATE_WRITER_DETAIL")
		}
		before := time.Now()
		h.ServeHTTP(w, sdRequest("POST", "/api/cameras/front_door/sd/recording", sdBody))
		if w.deadline.Before(before.Add(89*time.Second)) || w.deadline.After(time.Now().Add(91*time.Second)) {
			t.Fatal("response deadline not extended through logging wrapper")
		}
		expected := 501
		if fail {
			expected = 502
		}
		if w.Code != expected || strings.Contains(w.Body.String(), "PRIVATE") {
			t.Fatalf("deadline error: %d %s", w.Code, w.Body.String())
		}
	}
}
