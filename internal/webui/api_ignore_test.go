package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestHandleAPICameraAction_Ignore(t *testing.T) {
	// Ignoring a camera returns its record, drops it from the camera API,
	// lists it under /api/ignored and logs it. A second ignore and a GET
	// on the action are both 404s.
	srv, _ := testServer(t)
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	injectStreamingCamera(srv, "kitchen")

	req := httptest.NewRequest("POST", "/api/cameras/kitchen/ignore", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Status  string                `json:"status"`
		Ignored wyzeapi.IgnoredCamera `json:"ignored"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || resp.Ignored.MAC != "AABBCCK11223344" {
		t.Errorf("resp = %+v", resp)
	}

	// Gone from the camera API...
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("GET", "/api/cameras/kitchen", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("ignored camera still served: %d", w.Code)
	}
	// ...and listed as ignored.
	w = httptest.NewRecorder()
	srv.handleAPIIgnored(w, httptest.NewRequest("GET", "/api/ignored", nil))
	var list []wyzeapi.IgnoredCamera
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].MAC != "AABBCCK11223344" {
		t.Errorf("list = %+v", list)
	}
	// Logged for the metrics page.
	events := srv.Events().Snapshot()
	if len(events) != 1 || events[0].Kind != "ignore" || events[0].Camera != "kitchen" {
		t.Errorf("events = %+v", events)
	}

	// Ignoring it again is a 500 from the manager (unknown camera is
	// a 404 at the name lookup, which happens first).
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/ignore", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("second ignore: %d", w.Code)
	}
	// GET on the action is not an action.
	injectStreamingCamera(srv, "garage")
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("GET", "/api/cameras/garage/ignore", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("GET ignore: %d, want 404", w.Code)
	}
}

func TestHandleAPIIgnored_EmptyIsAnArrayNotNull(t *testing.T) {
	// With nothing ignored the list encodes as [] so the UI can loop over
	// it without a null check. The endpoint is read only (POST is 405).
	srv, _ := testServer(t)
	w := httptest.NewRecorder()
	srv.handleAPIIgnored(w, httptest.NewRequest("GET", "/api/ignored", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("empty list: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.handleAPIIgnored(w, httptest.NewRequest("POST", "/api/ignored", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/ignored: %d, want 405", w.Code)
	}
}

func TestHandleAPIIgnoredAction_Restore(t *testing.T) {
	// Restore takes the MAC in any case, takes the camera off the list,
	// kicks one rediscovery so it comes back now, and logs it. Unknown
	// MACs, wrong methods and malformed paths are refused.
	srv, _ := testServer(t)
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	srv.camMgr.SetIgnored([]wyzeapi.IgnoredCamera{{MAC: "D03F2700CA03", Nickname: "Driveway", Since: time.Now()}})
	var discovers atomic.Int32
	srv.OnDiscoverRequest(func(context.Context) { discovers.Add(1) })

	// MACs arrive however the UI spelled them; lowercase must work.
	req := httptest.NewRequest("POST", "/api/ignored/d03f2700ca03/restore", nil)
	w := httptest.NewRecorder()
	srv.handleAPIIgnoredAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if srv.camMgr.IsIgnored("D03F2700CA03") {
		t.Error("still ignored after restore")
	}
	deadline := time.Now().Add(2 * time.Second)
	for discovers.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if discovers.Load() != 1 {
		t.Errorf("restore should trigger exactly one rediscovery, got %d", discovers.Load())
	}
	if events := srv.Events().Snapshot(); len(events) != 1 || events[0].Kind != "ignore" || !strings.Contains(events[0].Message, "restored") {
		t.Errorf("events = %+v", events)
	}

	// Not on the list → 404; wrong method → 405; malformed path → 404.
	cases := []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/ignored/D03F2700CA03/restore", http.StatusNotFound},
		{"GET", "/api/ignored/D03F2700CA03/restore", http.StatusMethodNotAllowed},
		{"POST", "/api/ignored/D03F2700CA03", http.StatusNotFound},
		{"POST", "/api/ignored/D03F2700CA03/delete", http.StatusNotFound},
		{"POST", "/api/ignored//restore", http.StatusNotFound},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		srv.handleAPIIgnoredAction(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}

func TestHandleAPIIgnoredAction_NoDiscoverHook(t *testing.T) {
	// Without main's hook wired the restore still succeeds; the camera
	// just waits for the next scheduled refresh.
	srv, _ := testServer(t)
	srv.camMgr.SetIgnored([]wyzeapi.IgnoredCamera{{MAC: "AABB", Since: time.Now()}})
	w := httptest.NewRecorder()
	srv.handleAPIIgnoredAction(w, httptest.NewRequest("POST", "/api/ignored/AABB/restore", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRenderIndex_IgnoredStrip(t *testing.T) {
	// The dashboard shows the ignored strip only when something is
	// ignored, with a restore button per camera and the MAC standing in
	// for an unknown nickname. Every card gets an ignore button.
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "kitchen")

	// No ignored cameras: no strip, but every card has an ignore button.
	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if strings.Contains(body, `id="ignored"`) {
		t.Error("ignored strip rendered with nothing ignored")
	}
	if !strings.Contains(body, `class="ignore-btn" data-cam="kitchen"`) {
		t.Error("camera card lacks its ignore button")
	}

	srv.camMgr.SetIgnored([]wyzeapi.IgnoredCamera{
		{MAC: "D03F2700CA01", Nickname: "Front Porch Cam", Model: "WYZE_CAKP2JFUS", Since: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
		{MAC: "80482C0D6B02", Since: time.Now()}, // nickname unknown: show the MAC
	})
	w = httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body = w.Body.String()
	for _, want := range []string{`id="ignored"`, "Front Porch Cam", "V3", `data-mac="D03F2700CA01"`, `class="restore-btn"`, "<td>80482C0D6B02</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("ignored strip missing %q", want)
		}
	}
}
