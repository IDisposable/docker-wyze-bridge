package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzectl"
	"github.com/rs/zerolog"
)

type ptzCall struct{ method, path, src, body string }

// ptzServer wires a Server whose go2rtc points at a fake that records
// /api/wyze/* control calls.
func ptzServer(t *testing.T, offline bool) (*Server, *[]ptzCall) {
	t.Helper()
	srv, _ := testServer(t)
	var mu sync.Mutex
	var calls []ptzCall
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, ptzCall{r.Method, r.URL.Path, r.URL.Query().Get("src"), string(b)})
		mu.Unlock()
		if offline {
			http.Error(w, `stream "x" has no active wyze camera`, http.StatusNotFound)
			return
		}
		if r.URL.Path == "/api/wyze/ptz_state" {
			w.Write([]byte(`{"vertical":90,"horizontal":180}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fake.Close)
	srv.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(fake.URL, zerolog.Nop()))
	return srv, &calls
}

func injectCam(srv *Server, name, model string) {
	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: name, Model: model, MAC: "AABBCCDDEEFF", LanIP: "10.0.0.2"}, "hd", true, false)
	cam.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera(name, cam)
}

func doPTZ(srv *Server, method, cam, body string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, "/api/cameras/"+cam+"/ptz", rdr)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)
	return w
}

func TestPTZ_MoveAndStop(t *testing.T) {
	srv, calls := ptzServer(t, false)
	injectCam(srv, "pan", "HL_PAN3")

	if w := doPTZ(srv, "POST", "pan", `{"action":"move","direction":"left","speed":7}`); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if w := doPTZ(srv, "POST", "pan", `{"action":"move","direction":"stop"}`); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	if c := (*calls)[0]; c.path != "/api/wyze/rotate_action" || c.src != "pan" || c.body != `{"direction":1,"speed":7}` {
		t.Errorf("move call = %+v", c)
	}
	if c := (*calls)[1]; c.body != `{"direction":0,"speed":5}` {
		t.Errorf("stop call = %+v", c)
	}
}

func TestPTZ_GotoHomeAndState(t *testing.T) {
	srv, calls := ptzServer(t, false)
	injectCam(srv, "pan", "HL_PAN3")

	if w := doPTZ(srv, "POST", "pan", `{"action":"goto","vertical":90,"horizontal":180}`); w.Code != http.StatusOK {
		t.Fatalf("goto: %d %s", w.Code, w.Body)
	}
	if w := doPTZ(srv, "POST", "pan", `{"action":"home"}`); w.Code != http.StatusOK {
		t.Fatalf("home: %d %s", w.Code, w.Body)
	}
	w := doPTZ(srv, "GET", "pan", "")
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	var resp struct {
		Position struct{ Vertical, Horizontal int }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Position.Vertical != 90 || resp.Position.Horizontal != 180 {
		t.Errorf("state body = %s (%v)", w.Body, err)
	}
	paths := []string{}
	for _, c := range *calls {
		paths = append(paths, c.method+" "+c.path)
	}
	want := "POST /api/wyze/ptz_position,POST /api/wyze/reset_rotation,GET /api/wyze/ptz_state"
	if got := strings.Join(paths, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	if !strings.Contains((*calls)[0].body, `"v":90`) || !strings.Contains((*calls)[0].body, `"h":180`) {
		t.Errorf("goto body = %s", (*calls)[0].body)
	}
}

func TestPTZ_Validation(t *testing.T) {
	srv, calls := ptzServer(t, false)
	injectCam(srv, "pan", "HL_PAN3")
	injectCam(srv, "fixed", "WYZE_CAKP2JFUS")

	cases := []struct {
		cam, body string
		code      int
	}{
		{"fixed", `{"action":"move","direction":"left"}`, http.StatusBadRequest},
		{"pan", `{"action":"move","direction":"sideways"}`, http.StatusBadRequest},
		{"pan", `{"action":"move","direction":"left","speed":0}`, http.StatusBadRequest},
		{"pan", `{"action":"goto","vertical":200,"horizontal":10}`, http.StatusBadRequest},
		{"pan", `{"action":"goto","vertical":10}`, http.StatusBadRequest},
		{"pan", `{"action":"dance"}`, http.StatusBadRequest},
		{"pan", `not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := doPTZ(srv, "POST", tc.cam, tc.body); w.Code != tc.code {
			t.Errorf("%s %s: code = %d, want %d (%s)", tc.cam, tc.body, w.Code, tc.code, w.Body)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("invalid requests reached go2rtc: %+v", *calls)
	}
}

func TestPTZ_WebRTCPanRejected(t *testing.T) {
	srv, _ := ptzServer(t, false)
	injectCam(srv, "pan4", "HL_PAN4")
	if w := doPTZ(srv, "POST", "pan4", `{"action":"move","direction":"left"}`); w.Code != http.StatusNotImplemented {
		t.Errorf("code = %d, want 501", w.Code)
	}
}

func TestPTZ_CameraOffline(t *testing.T) {
	srv, _ := ptzServer(t, true)
	injectCam(srv, "pan", "HL_PAN3")
	if w := doPTZ(srv, "POST", "pan", `{"action":"move","direction":"up"}`); w.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409 (%s)", w.Code, w.Body)
	}
}

func TestCameraPage_PTZControlsOnlyForPanCams(t *testing.T) {
	srv, _ := ptzServer(t, false)
	injectCam(srv, "pan", "HL_PAN3")
	injectCam(srv, "fixed", "WYZE_CAKP2JFUS")
	for cam, want := range map[string]bool{"pan": true, "fixed": false} {
		w := httptest.NewRecorder()
		srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/"+cam, nil))
		if got := strings.Contains(w.Body.String(), `class="ptz"`); got != want {
			t.Errorf("%s: ptz controls rendered = %v, want %v", cam, got, want)
		}
	}
}

func TestPTZ_StepReadsThenGoes(t *testing.T) {
	srv, calls := ptzServer(t, false) // fake reports v=90 h=180
	injectCam(srv, "pan", "HL_PAN3")
	if w := doPTZ(srv, "POST", "pan", `{"action":"step","direction":"left","degrees":20}`); w.Code != http.StatusOK {
		t.Fatalf("step: %d %s", w.Code, w.Body)
	}
	if len(*calls) != 2 || (*calls)[0].path != "/api/wyze/ptz_state" || (*calls)[1].path != "/api/wyze/ptz_position" {
		t.Fatalf("calls = %+v", *calls)
	}
	if b := (*calls)[1].body; !strings.Contains(b, `"h":200`) || !strings.Contains(b, `"v":90`) {
		t.Errorf("goto body = %s, want v=90 h=200", b)
	}
	for _, body := range []string{`{"action":"step","direction":"stop"}`, `{"action":"step","direction":"up","degrees":0}`} {
		if w := doPTZ(srv, "POST", "pan", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", body, w.Code)
		}
	}
}

func TestStepPosition_Clamps(t *testing.T) {
	cases := []struct {
		pos wyzectl.PTZPosition
		dir wyzectl.RotationDirection
		deg int
		wv  uint8
		wh  uint16
	}{
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 180}, wyzectl.RotationLeft, 15, 90, 195},
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 180}, wyzectl.RotationRight, 15, 90, 165},
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 180}, wyzectl.RotationUp, 15, 75, 180},
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 180}, wyzectl.RotationDown, 15, 105, 180},
		{wyzectl.PTZPosition{Vertical: 5, Horizontal: 350}, wyzectl.RotationUp, 15, 0, 350},
		{wyzectl.PTZPosition{Vertical: 175, Horizontal: 350}, wyzectl.RotationDown, 15, 180, 350},
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 350}, wyzectl.RotationLeft, 15, 90, 359},
		{wyzectl.PTZPosition{Vertical: 90, Horizontal: 5}, wyzectl.RotationRight, 15, 90, 0},
	}
	for _, tc := range cases {
		v, h := stepPosition(tc.pos, tc.dir, tc.deg)
		if v != tc.wv || h != tc.wh {
			t.Errorf("stepPosition(%+v, %d, %d) = %d,%d want %d,%d", tc.pos, tc.dir, tc.deg, v, h, tc.wv, tc.wh)
		}
	}
}
