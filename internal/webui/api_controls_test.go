package webui

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// fakeCloud records what the handlers asked the Wyze cloud to do.
type fakeCloud struct {
	mu      sync.Mutex
	props   []string
	actions []string
	fail    error
}

func (f *fakeCloud) SetNamedProperty(cam wyzeapi.CameraInfo, property, raw string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	_, published, ok := wyzeapi.ParsePropertyValue(property, raw)
	if !ok {
		return "", errors.New("bad value")
	}
	f.props = append(f.props, cam.Name+":"+property+"="+raw)
	return published, nil
}

func (f *fakeCloud) RunAction(cam wyzeapi.CameraInfo, action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.actions = append(f.actions, cam.Name+":"+action)
	return nil
}

func TestHandleAPICameraAction_Property(t *testing.T) {
	// A property change goes to the cloud, and Wyze's normalized value is
	// what gets echoed, mirrored to MQTT and logged. Bad input is a 400
	// before the cloud is called; cloud failure is 502, no cloud is 503.
	srv, _ := testServer(t)
	cloud := &fakeCloud{}
	srv.cloud = cloud
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	injectStreamingCamera(srv, "kitchen")
	var mirrored []string
	done := make(chan struct{}, 4)
	srv.OnPropertySet(func(cam, prop, val string) { mirrored = append(mirrored, cam+":"+prop+"="+val); done <- struct{}{} })

	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/property", bytes.NewBufferString(`{"property":"hor_flip","value":"on"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"value":"1"`) {
		t.Errorf("response should carry Wyze's normalized value: %s", w.Body.String())
	}
	<-done
	if len(cloud.props) != 1 || cloud.props[0] != "kitchen:hor_flip=on" {
		t.Errorf("cloud got %v", cloud.props)
	}
	if len(mirrored) != 1 || mirrored[0] != "kitchen:hor_flip=1" {
		t.Errorf("MQTT mirror got %v", mirrored)
	}
	if ev := srv.Events().Snapshot(); len(ev) != 1 || ev[0].Kind != "command" {
		t.Errorf("events = %+v", ev)
	}

	// Validation happens before the cloud is touched.
	for _, body := range []string{`{"property":"nope","value":"on"}`, `{"property":"hor_flip","value":"sideways"}`, `not json`} {
		w := httptest.NewRecorder()
		srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/property", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, w.Code)
		}
	}
	if len(cloud.props) != 1 {
		t.Errorf("invalid requests reached the cloud: %v", cloud.props)
	}

	// Cloud failure → 502, nothing mirrored.
	cloud.fail = errors.New("wyze said no")
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/property", bytes.NewBufferString(`{"property":"ver_flip","value":"off"}`)))
	if w.Code != http.StatusBadGateway || len(mirrored) != 1 {
		t.Errorf("cloud failure: status=%d mirrored=%v", w.Code, mirrored)
	}

	// No cloud wired → 503.
	srv.cloud = nil
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/property", bytes.NewBufferString(`{"property":"ver_flip","value":"off"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no cloud: status = %d", w.Code)
	}
}

func TestHandleAPICameraAction_Reboot(t *testing.T) {
	// POST reboot sends one restart to the cloud and logs it. GET is not
	// an action (404), a cloud failure is a 502 and no cloud is a 503.
	srv, _ := testServer(t)
	cloud := &fakeCloud{}
	srv.cloud = cloud
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	injectStreamingCamera(srv, "kitchen")

	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/reboot", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(cloud.actions) != 1 || cloud.actions[0] != "kitchen:restart" {
		t.Errorf("cloud actions = %v", cloud.actions)
	}
	if ev := srv.Events().Snapshot(); len(ev) != 1 || !strings.Contains(ev[0].Message, "reboot") {
		t.Errorf("events = %+v", ev)
	}
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("GET", "/api/cameras/kitchen/reboot", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("GET reboot: %d, want 404", w.Code)
	}
	cloud.fail = errors.New("offline")
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/reboot", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("cloud failure: %d, want 502", w.Code)
	}
	srv.cloud = nil
	w = httptest.NewRecorder()
	srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/kitchen/reboot", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no cloud: %d, want 503", w.Code)
	}
}

func TestRenderCamera_Controls(t *testing.T) {
	// The detail page renders the flip and night vision buttons and the
	// reboot button with the data attributes app.js uses to send them.
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "kitchen")
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/kitchen", nil))
	body := w.Body.String()
	for _, want := range []string{
		`data-property="hor_flip" data-value="on"`,
		`data-property="ver_flip" data-value="off"`,
		`data-property="night_vision" data-value="auto"`,
		`class="reboot-btn danger" data-cam="kitchen"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
