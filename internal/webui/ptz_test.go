package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func (f *fakeCloud) MovePTZ(cam wyzeapi.CameraInfo, direction string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.ptz = append(f.ptz, cam.Name+":"+direction)
	return nil
}

func TestHandleAPICameraAction_PTZ(t *testing.T) {
	srv, _ := testServer(t)
	cloud := &fakeCloud{}
	srv.cloud = cloud
	cam := injectStreamingCamera(srv, "kitchen")
	info := cam.GetInfo()
	info.Model = "HL_PAN3"
	info.DTLS = true
	cam.UpdateInfo(info)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/kitchen/ptz", strings.NewReader(`{"direction":"left"}`))
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(cloud.ptz) != 1 || cloud.ptz[0] != "kitchen:left" {
		t.Errorf("PTZ commands = %v", cloud.ptz)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/cameras/kitchen/ptz", strings.NewReader(`{"direction":"diagonal"}`))
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid direction status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if len(cloud.ptz) != 1 {
		t.Errorf("invalid PTZ direction reached the commander: %v", cloud.ptz)
	}
}

func TestHandleAPICameraAction_PTZRejectsUnsupportedCamera(t *testing.T) {
	srv, _ := testServer(t)
	cloud := &fakeCloud{}
	srv.cloud = cloud
	injectStreamingCamera(srv, "kitchen")

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/kitchen/ptz", strings.NewReader(`{"direction":"left"}`))
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if len(cloud.ptz) != 0 {
		t.Errorf("unsupported camera reached the commander: %v", cloud.ptz)
	}
}

func TestRenderCamera_PTZControlsAreGated(t *testing.T) {
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "kitchen")
	info := cam.GetInfo()
	info.Model = "HL_PAN3"
	info.DTLS = true
	cam.UpdateInfo(info)

	w := httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest(http.MethodGet, "/camera/kitchen", nil))
	if !strings.Contains(w.Body.String(), `data-direction="up"`) {
		t.Fatal("PTZ controls are missing for a supported Pan camera")
	}
	if !strings.Contains(w.Body.String(), "Pan / tilt (hold)") {
		t.Fatal("PTZ controls are missing the hold-to-move label")
	}

	cam.SetForceWebRTC(true)
	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest(http.MethodGet, "/camera/kitchen", nil))
	if strings.Contains(w.Body.String(), `class="ptz-btn"`) {
		t.Fatal("PTZ controls rendered for a camera forced onto WebRTC")
	}
}
