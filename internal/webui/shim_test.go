package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// newShimReq builds a loopback request — the shim's requireLoopback
// middleware rejects anything else, so every test that exercises a
// handler directly needs this remote addr.
func newShimReq(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

// injectGwellCam adds a GW_BE1 (Doorbell Pro) to the manager.
func injectGwellCam(srv *Server, mac, name, lanIP string) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		Model: "GW_BE1",
		MAC:   mac,
		LanIP: lanIP,
	}, "hd", true, false)
	srv.camMgr.InjectCamera(name, cam)
}

func TestShim_CameraList_EmptyWhenNoGwell(t *testing.T) {
	srv, _ := testServer(t)
	// Inject a TUTK camera — should NOT appear in the list.
	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name: "front_door", Model: "HL_CAM4", MAC: "AABBCC112233",
	}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)

	w := httptest.NewRecorder()
	srv.handleShimCameraList(w, newShimReq("GET", "/internal/wyze/Camera/CameraList"))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got []string
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}

func TestShim_CameraList_OnlyGwell(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "10.0.0.5")
	// Add a TUTK camera — shouldn't appear.
	tutk := camera.NewCamera(wyzeapi.CameraInfo{
		Name: "tutk_cam", Model: "HL_CAM4", MAC: "112233445566",
	}, "hd", true, false)
	srv.camMgr.InjectCamera("tutk_cam", tutk)

	w := httptest.NewRecorder()
	srv.handleShimCameraList(w, newShimReq("GET", "/internal/wyze/Camera/CameraList"))

	var got []string
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 1 || got[0] != "BEBEBEBEBEBE" {
		t.Errorf("want [BEBEBEBEBEBE], got %v", got)
	}
}

func TestShim_DeviceInfo_Found(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "10.0.0.5")

	w := httptest.NewRecorder()
	srv.handleShimDeviceInfo(w, newShimReq("GET", "/internal/wyze/Camera/DeviceInfo?deviceId=BEBEBEBEBEBE"))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["cameraId"] != "BEBEBEBEBEBE" || got["streamName"] != "doorbell_pro" || got["lanIp"] != "10.0.0.5" {
		t.Errorf("unexpected payload: %v", got)
	}
}

func TestShim_DeviceInfo_CaseInsensitive(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "")

	w := httptest.NewRecorder()
	// lower-case MAC in the query string — should still resolve.
	srv.handleShimDeviceInfo(w, newShimReq("GET", "/internal/wyze/Camera/DeviceInfo?deviceId=bebebebebebe"))
	if w.Code != http.StatusOK {
		t.Errorf("expected case-insensitive lookup to succeed, got %d", w.Code)
	}
}

func TestShim_DeviceInfo_NotFound(t *testing.T) {
	srv, _ := testServer(t)
	w := httptest.NewRecorder()
	srv.handleShimDeviceInfo(w, newShimReq("GET", "/internal/wyze/Camera/DeviceInfo?deviceId=DEADBEEFDEAD"))
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
}

func TestShim_CameraToken_NoMinter(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "")

	w := httptest.NewRecorder()
	srv.handleShimCameraToken(w, newShimReq("GET", "/internal/wyze/Camera/CameraToken?deviceId=BEBEBEBEBEBE"))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 when Mars not wired", w.Code)
	}
}

// fakeMarsMinter lets us exercise the wired path without pulling in
// internal/wyzeapi's real Mars implementation (which doesn't exist yet).
type fakeMarsMinter struct {
	id, token string
	err       error
}

func (f *fakeMarsMinter) MarsRegisterGWUser(ctx context.Context, deviceID string) (string, string, error) {
	return f.id, f.token, f.err
}

func TestShim_CameraToken_Success(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "")
	srv.SetMarsMinter(&fakeMarsMinter{id: "12345", token: "abcdef"})

	w := httptest.NewRecorder()
	srv.handleShimCameraToken(w, newShimReq("GET", "/internal/wyze/Camera/CameraToken?deviceId=BEBEBEBEBEBE"))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["accessId"] != "12345" || got["accessToken"] != "abcdef" {
		t.Errorf("payload = %v", got)
	}
}

func TestShim_CameraToken_MarsError(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "")
	srv.SetMarsMinter(&fakeMarsMinter{err: errors.New("upstream 500")})

	w := httptest.NewRecorder()
	srv.handleShimCameraToken(w, newShimReq("GET", "/internal/wyze/Camera/CameraToken?deviceId=BEBEBEBEBEBE"))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502 when Mars returns error", w.Code)
	}
}

func TestShim_RequireLoopback_RejectsNonLoopback(t *testing.T) {
	srv, _ := testServer(t)
	injectGwellCam(srv, "BEBEBEBEBEBE", "doorbell_pro", "")

	wrapped := requireLoopback(srv.handleShimCameraList)

	// From a LAN address — middleware should 404, not invoke the handler.
	req := httptest.NewRequest("GET", "/internal/wyze/Camera/CameraList", nil)
	req.RemoteAddr = "192.168.1.50:12345"
	w := httptest.NewRecorder()
	wrapped(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("LAN caller got %d, want 404 (loopback-only)", w.Code)
	}
}
