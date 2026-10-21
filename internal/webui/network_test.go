package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// fixedResolver answers every Resolve with the same Result, so a test
// can put a camera's LAN status wherever it likes through the real
// manager path (the status fields are unexported on purpose).
type fixedResolver struct {
	res    lanresolve.Result
	engine string
}

func (f fixedResolver) Resolve(_ context.Context, t lanresolve.Target) lanresolve.Result {
	res := f.res
	if res.IP == "" {
		res.IP = t.CloudIP
	}
	return res
}
func (f fixedResolver) Check(context.Context, string, string) (bool, error) { return true, nil }
func (f fixedResolver) Engine() string                                      { return f.engine }

func TestShortDuration(t *testing.T) {
	// Durations print in the short form the Network box uses, with no
	// zero parts (2m, 1m30s, 1h).
	for in, want := range map[time.Duration]string{2 * time.Minute: "2m", 90 * time.Second: "1m30s", time.Hour: "1h", 0: "0s", 45 * time.Second: "45s", 36 * time.Hour: "36h"} {
		if got := shortDuration(in); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestLanLabels(t *testing.T) {
	// Each LAN source maps to its plain-words label, and the resolver
	// line reflects the engine, sweep and recheck settings, or says off
	// when LAN_IP_RESOLVE is false or no resolver is wired.
	for in, want := range map[string]string{
		"cloud": "the address the Wyze cloud reported", "last": "its last known address",
		"recent": "an earlier LAN sweep", "neigh": "the kernel's neighbor cache",
		"sweep": "a LAN sweep", "": "", "unverified": "",
	} {
		if got := LanSourceLabel(in); got != want {
			t.Errorf("LanSourceLabel(%q) = %q, want %q", in, got, want)
		}
	}

	srv, _ := testServer(t)
	srv.cfg.LanIPResolve, srv.cfg.LanIPSweep, srv.cfg.LanIPRecheck = true, true, 2*time.Minute
	if got := srv.lanResolverLabel(); got != "off (LAN_IP_RESOLVE=false)" {
		t.Errorf("no resolver wired: %q", got)
	}
	srv.SetLanResolver(fixedResolver{engine: "arp"})
	if got := srv.lanResolverLabel(); got != "ARP engine, sweep on, recheck every 2m" {
		t.Errorf("arp: %q", got)
	}
	srv.cfg.LanIPSweep, srv.cfg.LanIPRecheck = false, 0
	srv.SetLanResolver(fixedResolver{engine: "neigh"})
	if got := srv.lanResolverLabel(); !strings.HasPrefix(got, "neighbor-cache engine") || !strings.HasSuffix(got, "sweep off, recheck off") {
		t.Errorf("neigh: %q", got)
	}
	srv.SetLanResolver(fixedResolver{engine: "none"})
	if got := srv.lanResolverLabel(); !strings.HasPrefix(got, "no usable engine") {
		t.Errorf("none: %q", got)
	}
	srv.cfg.LanIPResolve = false
	if got := srv.lanResolverLabel(); got != "off (LAN_IP_RESOLVE=false)" {
		t.Errorf("disabled: %q", got)
	}
}

func TestRenderCamera_NetworkBox(t *testing.T) {
	// The Network box tracks a camera's LAN state: never resolved, found
	// by a sweep, then missed (keeping the last verified IP). A camera
	// that isn't dialed on the LAN only gets a not-used note.
	srv, _ := testServer(t)
	srv.cfg.LanIPResolve, srv.cfg.LanIPSweep, srv.cfg.LanIPRecheck = true, true, 2*time.Minute
	srv.SetLanResolver(fixedResolver{engine: "arp"})
	cam := injectStreamingCamera(srv, "garage")

	// Nothing resolved yet.
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/garage", nil))
	body := w.Body.String()
	if !strings.Contains(body, `class="network-box" data-cam="garage"`) {
		t.Fatal("detail page lacks the Network box")
	}
	for _, want := range []string{`class="cam-cloud-ip">10.0.0.1<`, `class="cam-lan-status">never<`, "ARP engine, sweep on, recheck every 2m"} {
		if !strings.Contains(body, want) {
			t.Errorf("fresh camera: missing %q", want)
		}
	}

	// The resolver found it at another address by sweeping: the box
	// says so, and the cloud's (stale) address is still shown.
	srv.camMgr.SetLANResolver(fixedResolver{res: lanresolve.Result{IP: "10.0.0.77", Verified: true, Source: "sweep"}})
	srv.camMgr.RestartStream(context.Background(), "garage")
	if cam.GetInfo().LanIP != "10.0.0.77" {
		t.Fatalf("setup: camera IP = %s", cam.GetInfo().LanIP)
	}
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/garage", nil))
	body = w.Body.String()
	for _, want := range []string{`class="cam-ip" data-cam="garage">10.0.0.77<`, `class="cam-cloud-ip">10.0.0.1<`, `class="cam-lan-status">just now via a LAN sweep<`} {
		if !strings.Contains(body, want) {
			t.Errorf("verified camera: missing %q", want)
		}
	}

	// Then it went missing: the history stays and the misses show.
	srv.camMgr.SetLANResolver(fixedResolver{res: lanresolve.Result{Verified: false, Source: "unverified", Misses: 3}})
	srv.camMgr.RestartStream(context.Background(), "garage")
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/garage", nil))
	body = w.Body.String()
	if !strings.Contains(body, "just now via a LAN sweep; 3 LAN sweeps in a row didn't hear it") {
		t.Errorf("missing camera: status row wrong:\n%s", between(body, `class="cam-lan-status">`, "</td>"))
	}
	if !strings.Contains(w.Body.String(), `class="cam-ip" data-cam="garage">10.0.0.77<`) {
		t.Error("an unverified resolve must not drop the earlier verified address")
	}

	// A camera that isn't dialed on the LAN (WebRTC doorbell) gets no
	// cloud/verified rows, just a note.
	srv.camMgr.InjectCamera("doorbell", camera.NewCamera(wyzeapi.CameraInfo{Name: "doorbell", Model: "GW_BE1", MAC: "AABBCCDD001122", LanIP: "10.0.0.9"}, "hd", true, false))
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/doorbell", nil))
	body = w.Body.String()
	if !strings.Contains(body, "not used: this camera isn't dialed on the LAN") || strings.Contains(body, "cam-cloud-ip") {
		t.Error("non-TUTK camera should only get the not-used note")
	}
}

// between returns the text between the first a and the following b,
// for readable failure messages.
func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}

func TestRenderIndex_LanHint(t *testing.T) {
	// The not-on-this-LAN hint stays hidden until two sweeps miss the
	// camera, then shows the count and an ignore button, and /api/cameras
	// reports lan_absent. Grid players also ask for MSE with a buffer.
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "garage")

	// Every card carries the hint, hidden until the sweeps give up.
	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `class="lan-hint" data-cam="garage" hidden>`) {
		t.Fatalf("hint should be rendered hidden:\n%s", between(body, `class="lan-hint"`, "</p>"))
	}

	// Two sweeps missed it: the hint shows with the count and an ignore
	// button that reuses the normal ignore action.
	srv.camMgr.SetLANResolver(fixedResolver{res: lanresolve.Result{Verified: false, Source: "unverified", Misses: 2}})
	srv.camMgr.RestartStream(context.Background(), "garage")
	w = httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body = w.Body.String()
	if !strings.Contains(body, `class="lan-hint" data-cam="garage">Not on this LAN? <span class="lan-hint-misses">2</span> sweeps didn't hear it.`) {
		t.Errorf("hint should be visible with the count:\n%s", between(body, `class="lan-hint"`, "</p>"))
	}
	if !strings.Contains(body, `class="ignore-btn hint" data-cam="garage"`) {
		t.Error("hint lacks its ignore button")
	}
	// /api/cameras carries the same verdict.
	w = httptest.NewRecorder()
	srv.handleAPICameras(w, httptest.NewRequest("GET", "/api/cameras", nil))
	if !strings.Contains(w.Body.String(), `"lan_absent":true`) {
		t.Errorf("/api/cameras should report lan_absent: %s", w.Body.String())
	}

	// One miss only: hidden again.
	srv.camMgr.SetLANResolver(fixedResolver{res: lanresolve.Result{Verified: false, Source: "unverified", Misses: 1}})
	srv.camMgr.RestartStream(context.Background(), "garage")
	w = httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `class="lan-hint" data-cam="garage" hidden>`) {
		t.Error("one miss should not show the hint")
	}
}

func TestHandleAPICameraAction_Pin(t *testing.T) {
	// Pinning an IP applies it, persists it, logs it and shows it on the
	// detail page; bad bodies change nothing and an empty IP unpins it.
	// Cameras that aren't dialed on the LAN can't be pinned.
	srv, _ := testServer(t)
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	cam := injectStreamingCamera(srv, "garage")
	var saved []map[string]string
	srv.camMgr.OnPinChange(func(pins map[string]string) { saved = append(saved, pins) })

	post := func(body string) (*httptest.ResponseRecorder, map[string]interface{}) {
		w := httptest.NewRecorder()
		srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/garage/pin", strings.NewReader(body)))
		var out map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	w, out := post(`{"ip":"10.0.0.200"}`)
	if w.Code != http.StatusOK || out["ip"] != "10.0.0.200" || out["pinned_ip"] != "10.0.0.200" {
		t.Fatalf("pin: %d %s", w.Code, w.Body.String())
	}
	if cam.GetInfo().LanIP != "10.0.0.200" || len(saved) != 1 {
		t.Errorf("pin not applied/persisted: ip=%s saved=%v", cam.GetInfo().LanIP, saved)
	}
	if ev := srv.Events().Snapshot(); len(ev) == 0 || ev[0].Kind != "lan_ip" || !strings.Contains(ev[0].Message, "pinned to 10.0.0.200") {
		t.Errorf("event log: %+v", ev)
	}

	// The detail page shows the pin in the form with the Clear button.
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/garage", nil))
	body := w.Body.String()
	for _, want := range []string{`class="pin-form" data-cam="garage"`, `class="pin-ip" name="ip" value="10.0.0.200"`, `class="unpin-btn">Clear`, `class="cam-ip" data-cam="garage">10.0.0.200<`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}

	for _, bad := range []string{`{"ip":"garage"}`, `{"ip":"fe80::1"}`, `not json`} {
		if w, _ := post(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, w.Code)
		}
	}
	if cam.GetInfo().LanIP != "10.0.0.200" || len(saved) != 1 {
		t.Error("rejected bodies must not change the pin")
	}

	// Clear.
	w, out = post(`{"ip":""}`)
	if w.Code != http.StatusOK || out["ip"] != "10.0.0.1" || out["pinned_ip"] != "" {
		t.Fatalf("unpin: %d %s", w.Code, w.Body.String())
	}
	if len(saved) != 2 || len(saved[1]) != 0 {
		t.Errorf("unpin persist: %v", saved)
	}
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/garage", nil))
	if !strings.Contains(w.Body.String(), `class="unpin-btn" hidden>Clear`) {
		t.Error("Clear button should be hidden without a pin")
	}

	// Not a LAN camera: refused.
	srv.camMgr.InjectCamera("doorbell", camera.NewCamera(wyzeapi.CameraInfo{Name: "doorbell", Model: "GW_BE1", MAC: "AABBCCDD001122", LanIP: "10.0.0.9"}, "hd", true, false))
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/doorbell/pin", strings.NewReader(`{"ip":"10.0.0.10"}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "TUTK") {
		t.Errorf("doorbell pin: %d %s", w.Code, w.Body.String())
	}
}
