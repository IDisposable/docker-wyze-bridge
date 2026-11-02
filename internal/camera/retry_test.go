package camera

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

// The other half of Driveway's twelve hours: once the failed dial is
// on the camera, something has to act on it. These pin down when the
// health poll restarts a stream and, just as much, when it doesn't.

// newRetryMgr is a manager whose go2rtc accepts every PUT and counts
// them, with one streaming V3 in it.
func newRetryMgr(t *testing.T) (*Manager, *Camera, func() int) {
	t.Helper()
	var mu sync.Mutex
	puts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			mu.Lock()
			puts++
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	mgr, _ := newLanIPTestManager(t, nil)
	mgr.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop()))
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	mgr.cameras[cam.Name()] = cam
	return mgr, cam, func() int {
		time.Sleep(50 * time.Millisecond) // the PUT runs in a goroutine
		mu.Lock()
		defer mu.Unlock()
		return puts
	}
}

func (c *Camera) retryState() (streak, stuck int, lastRestart time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.videoFailStreak, c.stuckPolls, c.lastRestart
}

func TestRetryIfStalled_UnhealedConnectErrorRestarts(t *testing.T) {
	// Connected fine, then the dial timed out and no video ever came.
	// One failed dial is enough; waiting for a consumer to show up in
	// go2rtc's table is what let it sit for hours, since go2rtc doesn't
	// list a consumer it can't feed.
	mgr, cam, puts := newRetryMgr(t)
	now := time.Now()
	cam.recordError("wyze: connect failed: discovery timeout", now)

	mgr.retryIfStalled(cam, false, 0, now)

	streak, _, restart := cam.retryState()
	if streak != 1 || restart.IsZero() {
		t.Errorf("streak=%d lastRestart=%v, want one restart", streak, restart)
	}
	if n := puts(); n != 1 {
		t.Errorf("go2rtc PUTs = %d, want 1", n)
	}
}

func TestRetryIfStalled_IdleCameraLeftAlone(t *testing.T) {
	// Most of the fleet sits idle with no consumers. go2rtc dials when
	// somebody asks, so that is not a fault and must never be restarted;
	// this is the regression that would hammer the cameras.
	mgr, cam, puts := newRetryMgr(t)
	now := time.Now()

	for i := 0; i < stuckPollsForRetry+3; i++ {
		mgr.retryIfStalled(cam, false, 0, now.Add(time.Duration(i)*30*time.Second))
	}

	if streak, _, restart := cam.retryState(); streak != 0 || !restart.IsZero() {
		t.Errorf("idle camera was restarted: streak=%d lastRestart=%v", streak, restart)
	}
	if n := puts(); n != 0 {
		t.Errorf("go2rtc PUTs = %d, want 0", n)
	}
}

func TestRetryIfStalled_ConsumersButNoVideoWaitsThenRestarts(t *testing.T) {
	// Somebody is watching (directly or through a copy) and go2rtc's
	// producer has no video track. A cold V3 can take 40s to answer, so
	// it gets stuckPollsForRetry polls before the restart.
	mgr, cam, _ := newRetryMgr(t)
	base := time.Now()

	for i := 0; i < stuckPollsForRetry-1; i++ {
		mgr.retryIfStalled(cam, false, 1, base.Add(time.Duration(i)*30*time.Second))
	}
	if _, _, restart := cam.retryState(); !restart.IsZero() {
		t.Fatalf("restarted after %d polls, want %d", stuckPollsForRetry-1, stuckPollsForRetry)
	}

	mgr.retryIfStalled(cam, false, 1, base.Add(time.Duration(stuckPollsForRetry)*30*time.Second))
	if _, stuck, restart := cam.retryState(); restart.IsZero() || stuck != 0 {
		t.Errorf("after %d stuck polls: lastRestart=%v stuckPolls=%d", stuckPollsForRetry, restart, stuck)
	}
}

func TestRetryIfStalled_VideoArrivingIsNotStuck(t *testing.T) {
	// Consumers attached and a video track present: nothing to do, and
	// the stuck counter must not creep up toward a spurious restart.
	mgr, cam, _ := newRetryMgr(t)
	base := time.Now()

	for i := 0; i < stuckPollsForRetry+2; i++ {
		mgr.retryIfStalled(cam, true, 2, base.Add(time.Duration(i)*30*time.Second))
	}

	if _, stuck, restart := cam.retryState(); stuck != 0 || !restart.IsZero() {
		t.Errorf("healthy stream disturbed: stuckPolls=%d lastRestart=%v", stuck, restart)
	}
}

func TestRetryIfStalled_SkipsCameraOffThisLAN(t *testing.T) {
	// Two sweeps running with no answer to its MAC means the camera
	// isn't on this network. A redial would fail the same way.
	mgr, cam, puts := newRetryMgr(t)
	now := time.Now()
	cam.recordError("wyze: connect failed: discovery timeout", now)
	cam.noteLanMisses(lanAbsentAfter)

	mgr.retryIfStalled(cam, false, 0, now)

	if _, _, restart := cam.retryState(); !restart.IsZero() {
		t.Error("retried a camera that isn't on this LAN")
	}
	if n := puts(); n != 0 {
		t.Errorf("go2rtc PUTs = %d, want 0", n)
	}
}

func TestRetryIfStalled_BackoffWidens(t *testing.T) {
	// A camera that keeps failing is retried less and less often, so a
	// camera that is really gone doesn't get a dial every poll.
	mgr, cam, _ := newRetryMgr(t)
	base := time.Now()
	cam.recordError("wyze: connect failed: discovery timeout", base)

	mgr.retryIfStalled(cam, false, 0, base)
	cam.mu.RLock()
	first := cam.retryBackoff()
	cam.mu.RUnlock()
	if first != 2*retryBackoffBase {
		t.Fatalf("gap after one failed restart = %v, want %v", first, 2*retryBackoffBase)
	}

	// Too soon for the next one.
	mgr.retryIfStalled(cam, false, 0, base.Add(first/2))
	if streak, _, _ := cam.retryState(); streak != 1 {
		t.Fatalf("retried inside the backoff: streak=%d", streak)
	}

	mgr.retryIfStalled(cam, false, 0, base.Add(first+time.Second))
	cam.mu.RLock()
	streak, second := cam.videoFailStreak, cam.retryBackoff()
	cam.mu.RUnlock()
	if streak != 2 {
		t.Fatalf("second retry didn't run: streak=%d", streak)
	}
	if second <= first || second > retryBackoffMax {
		t.Errorf("backoff went %v then %v, want wider and within %v", first, second, retryBackoffMax)
	}

	// The cap holds however long it goes on.
	cam.mu.Lock()
	cam.videoFailStreak = 50
	capped := cam.retryBackoff()
	cam.mu.Unlock()
	if capped != retryBackoffMax {
		t.Errorf("backoff after 50 failures = %v, want the cap %v", capped, retryBackoffMax)
	}
}

func TestRetryIfStalled_StaleErrorStopsRetrying(t *testing.T) {
	// go2rtc only errors when something pulls. Once the errors stop
	// coming the camera is just idle and on demand; retrying it forever
	// on the strength of one old failure is the churn the leave-idle-
	// cameras-alone rule exists to prevent.
	mgr, cam, _ := newRetryMgr(t)
	base := time.Now()
	cam.recordError("wyze: connect failed: discovery timeout", base)

	mgr.retryIfStalled(cam, false, 0, base.Add(errorFreshFor+time.Minute))

	if _, _, restart := cam.retryState(); !restart.IsZero() {
		t.Error("retried on an error that had gone stale")
	}
}

func TestObserveFlow_VideoClearsRetryState(t *testing.T) {
	// Video arriving is the all-clear: the backoff starts over, the
	// stuck count drops, and so does error_count.
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	cam.recordError("wyze: connect failed: discovery timeout", time.Now())
	cam.mu.Lock()
	cam.videoFailStreak, cam.stuckPolls = 3, 4
	cam.mu.Unlock()

	cam.observeFlow(true, 1, 1024, time.Now())

	streak, stuck, _ := cam.retryState()
	if streak != 0 || stuck != 0 || cam.Snapshot().ErrorCount != 0 {
		t.Errorf("video arriving left streak=%d stuck=%d errors=%d, want all 0", streak, stuck, cam.Snapshot().ErrorCount)
	}
}

func TestHealthCheck_RetriesACopyStreamsFailedDial(t *testing.T) {
	// End to end: the panel's ffmpeg copy pulls the camera, go2rtc
	// reports the dial failing against the panel, and the next health
	// poll puts the camera's stream back.
	mgr, cam, puts := newRetryMgr(t)
	mgr.noteCopyStreams(map[string]*go2rtcmgr.StreamInfo{
		"panel_driveway_bedb": {Producers: []go2rtcmgr.ProducerInfo{{URL: "rtsp://127.0.0.1:8554/driveway_bedb?video"}}},
	})
	mgr.RecordStreamError(t.Context(), "panel_driveway_bedb", "wyze: connect failed: discovery timeout")

	undialed := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{URL: cam.StreamURL()}}}
	mgr.observeVideo(cam, undialed, time.Now())

	if n := puts(); n != 1 {
		t.Errorf("go2rtc PUTs = %d, want the stream put back once", n)
	}
}

func TestStreamOk_FalseWhileDialsFail(t *testing.T) {
	// What /api/cameras and the WebUI read. "streaming" alone is not
	// health: it reads streaming for hours with no video behind it.
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	if !cam.StreamOk() {
		t.Fatal("a freshly connected camera should read ok")
	}

	cam.recordError("wyze: connect failed: discovery timeout", time.Now())
	if cam.StreamOk() {
		t.Error("stream_ok still true after a failed dial with no video")
	}
	if snap := cam.Snapshot(); snap.StreamOk {
		t.Error("Snapshot.StreamOk disagrees with StreamOk()")
	}
	if ok, _ := cam.StatusJSON()["stream_ok"].(bool); ok {
		t.Error("StatusJSON stream_ok disagrees with StreamOk()")
	}

	// Video arriving heals it.
	cam.observeFlow(true, 1, 4096, time.Now())
	if !cam.StreamOk() {
		t.Error("stream_ok still false once video is flowing")
	}

	// And no_video from the poll alone, with no error, is not ok.
	cam.observeFlow(true, 1, 4096, time.Now())
	cam.observeFlow(true, 1, 4096, time.Now())
	if cam.Video() != VideoNoVideo || cam.StreamOk() {
		t.Errorf("frozen stream: video=%v ok=%v, want no_video and not ok", cam.Video(), cam.StreamOk())
	}
}

func TestStreamOk_IdleAndStaleErrorReadOk(t *testing.T) {
	// An idle camera is on demand, not broken; and one carrying an old
	// error nothing has pulled on since isn't failing now.
	cam := newV3("driveway_bedb", "192.168.4.141")
	cam.SetState(StateStreaming)
	cam.observeFlow(false, 0, 0, time.Now())
	if cam.Video() != VideoIdle || !cam.StreamOk() {
		t.Errorf("idle: video=%v ok=%v", cam.Video(), cam.StreamOk())
	}
	cam.mu.Lock()
	cam.lastError = "wyze: connect failed: discovery timeout"
	cam.lastErrorAt = time.Now().Add(-errorFreshFor - time.Minute)
	cam.mu.Unlock()
	if !cam.StreamOk() {
		t.Error("a stale error should not keep a camera marked unhealthy")
	}
}
