package camera

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestVideo_String(t *testing.T) {
	// Video statuses render as the strings StatusJSON reports, and an
	// out-of-range value reads as unknown.
	for v, want := range map[Video]string{VideoUnknown: "unknown", VideoIdle: "idle", VideoLive: "live", VideoNoVideo: "no_video", Video(99): "unknown"} {
		if got := v.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", v, got, want)
		}
	}
}

func TestObserveFlow_Transitions(t *testing.T) {
	// Walks the video status through each case: idle with nobody pulling,
	// live once bytes move (or the counter restarts), no_video after two
	// frozen polls, and no_video when go2rtc never connects.
	cam := newV3("cam", "10.0.0.1")
	now := time.Now()
	step := func(connected bool, consumers, bytes int) Video {
		_, cur := cam.observeFlow(connected, consumers, bytes, now)
		return cur
	}

	// Nobody pulling, go2rtc not dialed: idle.
	if v := step(false, 0, 0); v != VideoIdle {
		t.Errorf("idle poll = %v", v)
	}
	// A consumer appears and go2rtc dials; no bytes yet on the first
	// poll — don't panic, keep the previous status.
	if v := step(true, 1, 0); v != VideoIdle {
		t.Errorf("first quiet poll should keep idle, got %v", v)
	}
	// Bytes arrive: live, and lastVideo is stamped.
	if v := step(true, 1, 5000); v != VideoLive || cam.Snapshot().LastVideo.IsZero() {
		t.Errorf("bytes arrived: %v", v)
	}
	// Counter frozen with a consumer attached: one poll of grace...
	if v := step(true, 1, 5000); v != VideoLive {
		t.Errorf("one stalled poll should keep live, got %v", v)
	}
	// ...then no video.
	if v := step(true, 1, 5000); v != VideoNoVideo {
		t.Errorf("two stalled polls = %v, want no_video", v)
	}
	// go2rtc reconnects and the counter restarts low: that's flow.
	if v := step(true, 1, 300); v != VideoLive {
		t.Errorf("counter reset should read as live, got %v", v)
	}
	// Consumer gone, producer dropped: idle again.
	if v := step(false, 0, 0); v != VideoIdle {
		t.Errorf("back to idle: %v", v)
	}
	// Consumer pulling but go2rtc never connected (dial failing).
	step(false, 1, 0)
	if v := step(false, 1, 0); v != VideoNoVideo {
		t.Errorf("pulled but never connected = %v, want no_video", v)
	}
}

func TestResetFlow_OnLeavingStreaming(t *testing.T) {
	// Leaving streaming (an error or going offline) resets the video
	// status and poll counters so old stall counts don't carry over, but
	// lastVideo is kept as history.
	cam := newV3("cam", "10.0.0.1")
	cam.SetState(StateStreaming)
	cam.observeFlow(true, 1, 100, time.Now())
	cam.observeFlow(true, 1, 200, time.Now())
	if cam.Video() != VideoLive {
		t.Fatal("setup: should be live")
	}
	cam.IncrementError()
	if cam.Video() != VideoUnknown {
		t.Errorf("IncrementError should reset video to unknown, got %v", cam.Video())
	}
	if cam.Snapshot().LastVideo.IsZero() {
		t.Error("lastVideo is history and must survive a reset")
	}
	cam.SetState(StateStreaming)
	cam.SetState(StateOffline)
	if cam.Video() != VideoUnknown || cam.lastBytes != 0 || cam.stalledPolls != 0 {
		t.Error("SetState(offline) should reset the poll bookkeeping")
	}
}

// go2rtcFlowMock serves /api/streams with a controllable per-stream
// producer byte count and consumer count.
type go2rtcFlowMock struct {
	mu        sync.Mutex
	bytes     map[string]int
	consumers map[string]int
	connected map[string]bool
}

func newFlowMock(t *testing.T) (*go2rtcFlowMock, *go2rtcmgr.APIClient) {
	t.Helper()
	m := &go2rtcFlowMock{bytes: map[string]int{}, consumers: map[string]int{}, connected: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streams" {
			w.WriteHeader(404)
			return
		}
		if r.Method != "GET" {
			w.WriteHeader(200)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		out := map[string]interface{}{}
		for name := range m.connected {
			prod := map[string]interface{}{"url": "wyze://10.0.0.1"}
			if m.connected[name] {
				prod["remote_addr"] = "10.0.0.1:32761"
				prod["receivers"] = []map[string]int{{"bytes": m.bytes[name]}}
			}
			var cons []map[string]interface{}
			for i := 0; i < m.consumers[name]; i++ {
				cons = append(cons, map[string]interface{}{"format_name": "rtsp"})
			}
			out[name] = map[string]interface{}{"producers": []interface{}{prod}, "consumers": cons}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return m, go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
}

func (m *go2rtcFlowMock) set(name string, connected bool, consumers, bytes int) {
	m.mu.Lock()
	m.connected[name], m.consumers[name], m.bytes[name] = connected, consumers, bytes
	m.mu.Unlock()
}

func TestHealthCheck_TracksVideoStatus(t *testing.T) {
	// HealthCheck feeds go2rtc's byte counts into the video status and
	// fires one OnVideoChange per transition without touching the state
	// machine. A stream go2rtc lost entirely still errors the camera.
	mock, api := newFlowMock(t)
	cfg := &config.Config{Quality: "hd", Audio: true, CamOverrides: map[string]config.CamOverride{}, RefreshInterval: time.Hour}
	mgr := NewManager(cfg, wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop()), api, zerolog.Nop())
	cam := newV3("side_garage", "10.0.0.1")
	cam.SetState(StateStreaming)
	mgr.cameras[cam.Name()] = cam
	var changes []string
	mgr.OnVideoChange(func(c *Camera, old, cur Video) { changes = append(changes, old.String()+">"+cur.String()) })
	ctx := context.Background()

	mock.set("side_garage", false, 0, 0)
	mgr.HealthCheck(ctx)
	mock.set("side_garage", true, 1, 1000)
	mgr.HealthCheck(ctx)
	mock.set("side_garage", true, 1, 1000) // frozen
	mgr.HealthCheck(ctx)
	mgr.HealthCheck(ctx)
	mock.set("side_garage", true, 1, 9000)
	mgr.HealthCheck(ctx)

	want := []string{"unknown>idle", "idle>live", "live>no_video", "no_video>live"}
	if len(changes) != len(want) {
		t.Fatalf("changes = %v, want %v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Errorf("[%d] = %s, want %s", i, changes[i], want[i])
		}
	}
	if cam.GetState() != StateStreaming {
		t.Errorf("video tracking must not touch the state machine: %v", cam.GetState())
	}
	st := cam.StatusJSON()
	if st["video"] != "live" || st["last_video"].(time.Time).IsZero() {
		t.Errorf("StatusJSON video=%v last_video=%v", st["video"], st["last_video"])
	}

	// A stream go2rtc lost entirely still goes through the existing
	// producers==0 → error path, not the video path.
	mock.mu.Lock()
	delete(mock.connected, "side_garage")
	mock.mu.Unlock()
	mgr.HealthCheck(ctx)
	if cam.GetState() != StateError {
		t.Errorf("missing stream should error the camera, got %v", cam.GetState())
	}
	if cam.Video() != VideoUnknown {
		t.Errorf("error state should reset video, got %v", cam.Video())
	}
}

func TestRecordStreamError(t *testing.T) {
	// A go2rtc stream error lands on the camera and flips it to no_video.
	// A TUTK discovery timeout also kicks one LAN recheck (not two inside
	// the gap); other errors and WebRTC cameras are recorded without one.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "side_garage", "10.0.0.1")
	bell := NewCamera(wyzeapi.CameraInfo{Name: "front_door", Model: "GW_DBD", MAC: "GW_DBD_80482C0D6B01"}, "hd", true, false)
	bell.SetState(StateStreaming)
	mgr.cameras["front_door"] = bell

	var errs, changes []string
	mgr.OnStreamError(func(c *Camera, msg string) { errs = append(errs, c.Name()+": "+msg) })
	mgr.OnVideoChange(func(c *Camera, old, cur Video) { changes = append(changes, c.Name()+":"+old.String()+">"+cur.String()) })
	checksBefore := len(r.checkCalls())
	ctx := context.Background()

	mgr.RecordStreamError(ctx, "side_garage", "wyze: connect failed: discovery timeout")

	snap := cam.Snapshot()
	if snap.LastError != "wyze: connect failed: discovery timeout" || snap.LastErrorAt.IsZero() || snap.Video != VideoNoVideo {
		t.Errorf("snapshot = video:%v err:%q at:%v", snap.Video, snap.LastError, snap.LastErrorAt)
	}
	if len(errs) != 1 || len(changes) != 1 || changes[0] != "side_garage:unknown>no_video" {
		t.Errorf("errs=%v changes=%v", errs, changes)
	}
	// A discovery timeout on a TUTK camera triggers an immediate LAN
	// recheck (one Check), but not twice inside the gap.
	if got := len(r.checkCalls()) - checksBefore; got != 1 {
		t.Errorf("rechecks after timeout = %d, want 1", got)
	}
	mgr.RecordStreamError(ctx, "side_garage", "wyze: connect failed: discovery timeout")
	if got := len(r.checkCalls()) - checksBefore; got != 1 {
		t.Errorf("second timeout inside the gap should not recheck again, got %d", got)
	}
	if len(changes) != 1 {
		t.Errorf("no_video → no_video must not fire a change: %v", changes)
	}

	// Other errors record but don't recheck; WebRTC cameras never do.
	mgr.RecordStreamError(ctx, "side_garage", "av login failed")
	mgr.RecordStreamError(ctx, "front_door", "websocket: close 1006 (abnormal closure): unexpected EOF")
	if got := len(r.checkCalls()) - checksBefore; got != 1 {
		t.Errorf("non-timeout errors triggered rechecks: %d", got)
	}
	if bell.Snapshot().LastError == "" || bell.Video() != VideoNoVideo {
		t.Error("doorbell error not recorded")
	}
	// Unknown stream names (go2rtc's own internal streams) are ignored.
	mgr.RecordStreamError(ctx, "nope", "whatever")
	if len(errs) != 4 {
		t.Errorf("errs = %v", errs)
	}
	// Flow coming back clears the status but keeps the error as history.
	cam.observeFlow(true, 1, 100, time.Now())
	if cam.Video() != VideoLive || cam.Snapshot().LastError == "" {
		t.Error("recovery should be live with the error kept as history")
	}
}

func TestRecordStreamError_TimeoutMovesCamera(t *testing.T) {
	// The whole point: a discovery timeout on a camera that changed
	// address gets it re-registered right away.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, srcOf := newLanIPTestManager(t, r)
	streamingV3(t, mgr, "side_garage", "192.168.4.123")
	r.setResult(lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "sweep"}, false)

	mgr.RecordStreamError(context.Background(), "side_garage", "wyze: connect failed: discovery timeout")

	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.151?") {
		t.Errorf("go2rtc should have the new address, has %q", src)
	}
}

func TestObserveVideo_BrowserPullerCountsAsConsumer(t *testing.T) {
	// go2rtc lists no consumer for a stream it can't feed, so the
	// dashboard's open socket is what turns "idle" into "no video".
	mgr, _ := newTestManager(t)
	cam := NewCamera(wyzeapi.CameraInfo{Name: "attic", Model: "WYZE_CAKP2JFUS", MAC: "AABBCCDDEE01", LanIP: "10.0.0.9"}, "hd", true, false)
	cam.SetState(StateStreaming)
	now := time.Now()
	notConnected := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{URL: "wyze://10.0.0.9"}}}

	mgr.observeVideo(cam, notConnected, now)
	mgr.observeVideo(cam, notConnected, now.Add(30*time.Second))
	if cam.Video() != VideoIdle {
		t.Fatalf("nobody pulling: %v, want idle", cam.Video())
	}
	cam.AddPuller(1)
	mgr.observeVideo(cam, notConnected, now.Add(60*time.Second))
	mgr.observeVideo(cam, notConnected, now.Add(90*time.Second))
	if cam.Video() != VideoNoVideo {
		t.Errorf("browser pulling, nothing arriving: %v, want no_video", cam.Video())
	}
	cam.AddPuller(-1)
	cam.AddPuller(-1) // never below zero
	if cam.Pullers() != 0 {
		t.Errorf("pullers = %d", cam.Pullers())
	}
	mgr.observeVideo(cam, notConnected, now.Add(120*time.Second))
	if cam.Video() != VideoIdle {
		t.Errorf("socket gone: %v, want idle", cam.Video())
	}
}
