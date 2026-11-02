package camera

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Driveway on the NAS: its first dial failed and the bridge called it
// healthy for twelve hours. go2rtc reported the failure against the
// dashboard panel's copy of the stream, the snapshot paths logged
// their failures and dropped them, and recordError never touched
// error_count. These pin down each of those.

func newAttributionMgr(t *testing.T) (*Manager, *Camera) {
	t.Helper()
	_, api := newFlowMock(t)
	cfg := &config.Config{Quality: "hd", Audio: true, CamOverrides: map[string]config.CamOverride{}, RefreshInterval: time.Hour}
	mgr := NewManager(cfg, wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop()), api, zerolog.Nop())
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	mgr.cameras[cam.Name()] = cam
	return mgr, cam
}

func TestRecordError_CountsConnectFailuresOnly(t *testing.T) {
	// error_count reading 0 while every dial times out is the lie this
	// stops. A complaint that isn't about the dial still doesn't count,
	// and neither kind moves the state machine.
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)

	cam.recordError("wyze: connect failed: discovery timeout", time.Now())
	if got := cam.Snapshot().ErrorCount; got != 1 {
		t.Errorf("error_count = %d after a failed dial, want 1", got)
	}
	cam.recordError("websocket: close 1006 (abnormal closure): unexpected EOF", time.Now())
	if got := cam.Snapshot().ErrorCount; got != 1 {
		t.Errorf("error_count = %d, want 1 (only a failed dial counts)", got)
	}
	if got, _ := cam.StatusJSON()["error_count"].(int); got != 1 {
		t.Errorf("StatusJSON error_count = %d, want 1", got)
	}
	if cam.GetState() != StateStreaming {
		t.Errorf("recordError must not move the state machine: %v", cam.GetState())
	}
	// The state machine's own count, which sets the reconnect backoff
	// and the chronic report, is not what a failed dial bumps.
	if cam.GetErrorCount() != 0 || cam.BackoffDuration() != 5*time.Second {
		t.Errorf("failed dials leaked into the reconnect backoff: count=%d backoff=%v", cam.GetErrorCount(), cam.BackoffDuration())
	}
}

func TestObserveFlow_VideoClearsErrorCount(t *testing.T) {
	// Video arriving means the camera is fine now, whatever the count
	// says; lastError is history and stays.
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	cam.recordError("wyze: connect failed: discovery timeout", time.Now())
	cam.recordError("wyze: connect failed: discovery timeout", time.Now())

	cam.observeFlow(true, 1, 1024, time.Now())

	snap := cam.Snapshot()
	if snap.ErrorCount != 0 || snap.Video != VideoLive {
		t.Errorf("after video: error_count=%d video=%v, want 0 and live", snap.ErrorCount, snap.Video)
	}
	if snap.LastError == "" {
		t.Error("lastError is history and must survive video arriving")
	}
}

func TestNoteCopyStreams_RealShapes(t *testing.T) {
	// The three producer shapes go2rtc 1.9.14 shows for a copy stream
	// on the NAS: an exec ffmpeg leg once it's running (url is the RTSP
	// pull, source is the command line), a bare ffmpeg: source before
	// it has dialed, and one that mentions no camera at all.
	mgr, _ := newAttributionMgr(t)
	bedb := newV3("bedb", "192.168.4.9")
	mgr.cameras[bedb.Name()] = bedb
	petCam := newV3("pet_cam", "192.168.4.120")
	mgr.cameras[petCam.Name()] = petCam
	petCam2 := newV3("pet_cam2", "192.168.4.137")
	mgr.cameras[petCam2.Name()] = petCam2

	mgr.noteCopyStreams(map[string]*go2rtcmgr.StreamInfo{
		"driveway_bedb": {Name: "driveway_bedb", Producers: []go2rtcmgr.ProducerInfo{{Source: "wyze://192.168.4.141?uid=x"}}},
		"panel_driveway_bedb": {Name: "panel_driveway_bedb", Producers: []go2rtcmgr.ProducerInfo{{
			URL:    "rtsp://127.0.0.1:8554/driveway_bedb?video",
			Source: `exec:ffmpeg -hide_banner -i rtsp://127.0.0.1:8554/driveway_bedb?video -c:v mjpeg -an -f mjpeg -`,
		}}},
		"panel_front_door": {Name: "panel_front_door", Producers: []go2rtcmgr.ProducerInfo{{URL: "ffmpeg:front_door#video=mjpeg#width=480"}}},
		"panel_pet_cam2":   {Name: "panel_pet_cam2", Producers: []go2rtcmgr.ProducerInfo{{URL: "rtsp://127.0.0.1:8554/pet_cam2?video"}}},
		"doorbell_clip":    {Name: "doorbell_clip", Producers: []go2rtcmgr.ProducerInfo{{URL: "ffmpeg:/media/clips/front.mp4"}}},
		"unknown":          nil,
	})

	want := map[string]string{
		"panel_driveway_bedb": "driveway_bedb", // not "bedb": whole words only
		"panel_pet_cam2":      "pet_cam2",      // not "pet_cam"
	}
	mgr.mu.RLock()
	got := mgr.copies
	mgr.mu.RUnlock()
	if len(got) != len(want) {
		t.Errorf("copies = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("copies[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestResolveStreamName(t *testing.T) {
	mgr, cam := newAttributionMgr(t)
	bedb := newV3("bedb", "192.168.4.9")
	mgr.cameras[bedb.Name()] = bedb

	if got := mgr.resolveStreamName(cam.Name()); got != "driveway_bedb" {
		t.Errorf("a camera's own name resolved to %q", got)
	}
	// Before any health poll, a _<camera> suffix carries it, and the
	// longer camera name wins over the shorter one. A bare suffix
	// doesn't: "mybedb" is not a copy of bedb.
	if got := mgr.resolveStreamName("panel_driveway_bedb"); got != "driveway_bedb" {
		t.Errorf("suffix match gave %q, want driveway_bedb", got)
	}
	if got := mgr.resolveStreamName("mybedb"); got != "" {
		t.Errorf("bare suffix matched %q, want nothing", got)
	}
	// After a poll, the producer's source decides.
	mgr.noteCopyStreams(map[string]*go2rtcmgr.StreamInfo{
		"feed7": {Name: "feed7", Producers: []go2rtcmgr.ProducerInfo{{URL: "ffmpeg:driveway_bedb#video=mjpeg"}}},
	})
	if got := mgr.resolveStreamName("feed7"); got != "driveway_bedb" {
		t.Errorf("learned copy gave %q, want driveway_bedb", got)
	}
	if got := mgr.resolveStreamName("nothing_like_it"); got != "" {
		t.Errorf("unrelated stream resolved to %q", got)
	}
}

func TestRecordStreamError_CopyStreamCountsAgainstTheCamera(t *testing.T) {
	mgr, cam := newAttributionMgr(t)
	var errs []string
	mgr.OnStreamError(func(c *Camera, msg string) { errs = append(errs, c.Name()) })

	mgr.RecordStreamError(context.Background(), "panel_driveway_bedb", "wyze: connect failed: discovery timeout")

	snap := cam.Snapshot()
	if snap.ErrorCount != 1 || snap.LastError == "" || snap.LastErrorAt.IsZero() || snap.Video != VideoNoVideo {
		t.Errorf("snapshot = errors:%d err:%q at:%v video:%v", snap.ErrorCount, snap.LastError, snap.LastErrorAt, snap.Video)
	}
	if len(errs) != 1 || errs[0] != "driveway_bedb" {
		t.Errorf("OnStreamError fired for %v, want driveway_bedb once", errs)
	}
}

func TestHealthCheck_LearnsCopyStreamsFromTheTable(t *testing.T) {
	// The real path: the health poll lists go2rtc's table, sees the
	// panel's producer pulling the camera's stream, and from then on
	// an error against the panel's name lands on the camera.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streams" || r.Method != "GET" {
			w.WriteHeader(200)
			return
		}
		fmt.Fprint(w, `{
		  "driveway_bedb": {"producers": [{"url": "wyze://192.168.4.141?uid=x"}]},
		  "cam7": {"producers": [{"url": "rtsp://127.0.0.1:8554/driveway_bedb?video", "source": "exec:ffmpeg -i rtsp://127.0.0.1:8554/driveway_bedb?video -f mjpeg -"}]}
		}`)
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{Quality: "hd", Audio: true, CamOverrides: map[string]config.CamOverride{}, RefreshInterval: time.Hour}
	mgr := NewManager(cfg, wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop()), go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop()), zerolog.Nop())
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	mgr.cameras[cam.Name()] = cam

	mgr.RecordStreamError(context.Background(), "cam7", "wyze: connect failed: discovery timeout")
	if cam.Snapshot().ErrorCount != 0 {
		t.Fatal("nothing should land before the copy is known; cam7 doesn't even end in the camera's name")
	}
	mgr.HealthCheck(context.Background())
	mgr.RecordStreamError(context.Background(), "cam7", "wyze: connect failed: discovery timeout")
	if got := cam.Snapshot().ErrorCount; got != 1 {
		t.Errorf("error_count = %d after the poll learned the copy, want 1", got)
	}
}

func TestRecordSnapshotError(t *testing.T) {
	// go2rtc answers a snapshot it can't serve with a 500 carrying the
	// dial error, which counts like any failed dial. Our own client
	// timing out says only that no frame came in time (a dozing V3
	// can take 40s to answer) and a request the browser gave up on is
	// nobody's fault; neither touches the camera.
	mgr, cam := newAttributionMgr(t)
	ctx := context.Background()

	mgr.RecordSnapshotError(ctx, cam.Name(), fmt.Errorf(`get snapshot "driveway_bedb": %w`, &go2rtcmgr.StatusError{Status: 500, Body: "streams: wyze: connect failed: discovery timeout"}))
	snap := cam.Snapshot()
	if snap.ErrorCount != 1 || snap.LastError != "snapshot: wyze: connect failed: discovery timeout" {
		t.Errorf("500 from go2rtc: errors=%d err=%q", snap.ErrorCount, snap.LastError)
	}

	cam.observeFlow(true, 1, 4096, time.Now())
	for _, err := range []error{
		fmt.Errorf(`get snapshot "driveway_bedb": %w`, context.DeadlineExceeded),
		errors.New(`get snapshot "driveway_bedb": Get "http://127.0.0.1:1984/api/frame.jpeg?src=driveway_bedb": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
		fmt.Errorf(`get snapshot "driveway_bedb": %w`, context.Canceled),
	} {
		mgr.RecordSnapshotError(ctx, cam.Name(), err)
		if snap = cam.Snapshot(); snap.Video != VideoLive || snap.ErrorCount != 0 {
			t.Errorf("%v: marked the camera (video=%v errors=%d err=%q)", err, snap.Video, snap.ErrorCount, snap.LastError)
		}
	}
}
