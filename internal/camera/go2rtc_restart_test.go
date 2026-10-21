package camera

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
)

func TestReregister_PutsEveryStreamBack(t *testing.T) {
	// go2rtc restarted: the new process knows none of the runtime
	// streams. Reregister adds each one again and the cameras end up
	// streaming, including one that errored while go2rtc was down.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	a := streamingV3(t, mgr, "pet_cam", "192.168.4.20")
	b := streamingV3(t, mgr, "side_yard", "192.168.4.139")
	b.IncrementError()
	resolves := len(r.resolveCalls())

	fresh, srcOf := recordingGo2RTC(t)
	mgr.SetGo2RTCAPI(fresh)
	mgr.Reregister(context.Background())

	for _, cam := range []*Camera{a, b} {
		if srcOf(cam.Name()) != cam.StreamURL() {
			t.Errorf("%s: not re-added (src %q)", cam.Name(), srcOf(cam.Name()))
		}
		if cam.GetState() != StateStreaming {
			t.Errorf("%s: state = %v", cam.Name(), cam.GetState())
		}
	}
	if got := len(r.resolveCalls()); got != resolves {
		t.Errorf("resolver asked %d more times; addresses didn't move", got-resolves)
	}
}

func TestRegisterStream_Go2RTCDownIsNotACameraFailure(t *testing.T) {
	// While go2rtc was down every AddStream failed with "connection
	// refused". Those must not count toward the TUTK → WebRTC fallback:
	// a long enough outage would otherwise move healthy V3s to WebRTC.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listening now

	mgr, _ := newLanIPTestManager(t, nil)
	mgr.cfg.TUTKFallbackThreshold = 2
	mgr.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(url, zerolog.Nop()))
	cam := newV3("pet_cam", "192.168.4.20")
	mgr.cameras["pet_cam"] = cam

	for range 4 {
		mgr.registerStream(context.Background(), mgr.go2rtcClient(), cam)
	}
	if cam.GetState() != StateError {
		t.Errorf("state = %v, want error", cam.GetState())
	}
	if cam.ForceWebRTC() {
		t.Error("go2rtc being down promoted the camera to WebRTC")
	}
}

func TestUnreachable(t *testing.T) {
	// A 500 means go2rtc answered, so it isn't unreachable; a closed port
	// is. registerStream relies on this to keep a go2rtc outage from
	// counting toward the TUTK fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	err := api.AddStream(context.Background(), "x", "rtsp://x")
	if err == nil || go2rtcmgr.Unreachable(err) {
		t.Errorf("a 500 is go2rtc answering, not unreachable: %v", err)
	}
	srv.Close()
	err = api.AddStream(context.Background(), "x", "rtsp://x")
	if !go2rtcmgr.Unreachable(err) {
		t.Errorf("closed port should be unreachable: %v", err)
	}
}
