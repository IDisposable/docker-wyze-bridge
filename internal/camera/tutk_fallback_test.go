package camera

import (
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// recordTUTKFailure is the promotion primitive; these tests exercise
// it in isolation from the connect/health-check plumbing so a broken
// stream mock can't obscure the state-machine logic. See
// DOCS/TUTK_WEBRTC_FALLBACK_DESIGN.md.

func newTUTKCam(t *testing.T, name, model string) *Camera {
	t.Helper()
	return NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		Model: model,
		MAC:   "AABBCCDD" + name,
	}, "hd", true, false)
}

func TestRecordTUTKFailure_PromotesAtThreshold(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.TUTKFallbackThreshold = 3
	cam := newTUTKCam(t, "v4", "HL_CAM4")
	mgr.cameras["v4"] = cam

	var fallbacks []string
	mgr.OnProtocolFallback(func(camName, oldP, newP string, streak int) {
		if oldP != "tutk" || newP != "webrtc" || streak != 3 {
			t.Errorf("callback args: cam=%s old=%s new=%s streak=%d", camName, oldP, newP, streak)
		}
		fallbacks = append(fallbacks, camName)
	})

	for i := 1; i <= 3; i++ {
		mgr.recordTUTKFailure(cam, "tutk")
		if i < 3 && cam.ForceWebRTC() {
			t.Fatalf("promoted early (after %d failures, threshold=3)", i)
		}
	}
	if !cam.ForceWebRTC() {
		t.Fatal("should have been promoted at threshold=3")
	}
	if len(fallbacks) != 1 {
		t.Errorf("callback should fire exactly once, got %d", len(fallbacks))
	}
	// After promotion, streamSourceFor returns the WebRTC URL.
	u, p := mgr.streamSourceFor(cam)
	if p != "webrtc" {
		t.Errorf("post-promotion protocol = %q, want webrtc", p)
	}
	if u == "" {
		t.Error("post-promotion URL should be non-empty (webrtc:… source)")
	}
}

func TestRecordTUTKFailure_ResetsOnSuccessfulStreaming(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.TUTKFallbackThreshold = 5
	cam := newTUTKCam(t, "v4", "HL_CAM4")

	// 4 failures then a successful Streaming — streak resets.
	for i := 0; i < 4; i++ {
		mgr.recordTUTKFailure(cam, "tutk")
	}
	if cam.TUTKFailStreak() != 4 {
		t.Fatalf("streak = %d, want 4 before reset", cam.TUTKFailStreak())
	}
	cam.SetState(StateStreaming)
	if cam.TUTKFailStreak() != 0 {
		t.Fatalf("streak should reset on Streaming, got %d", cam.TUTKFailStreak())
	}
	// Now 4 more failures — should NOT promote (would take a full 5).
	for i := 0; i < 4; i++ {
		mgr.recordTUTKFailure(cam, "tutk")
	}
	if cam.ForceWebRTC() {
		t.Error("should not promote — streak reset after streaming means it takes another 5")
	}
}

func TestRecordTUTKFailure_IgnoresNonTUTKProtocols(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.TUTKFallbackThreshold = 2

	// A Gwell camera (Window Cam) — its failure surface is different
	// (gwell-proxy handshake, not go2rtc TUTK). No promotion.
	gwell := newTUTKCam(t, "wc", "GW_WC")
	for i := 0; i < 10; i++ {
		mgr.recordTUTKFailure(gwell, "gwell")
	}
	if gwell.TUTKFailStreak() != 0 || gwell.ForceWebRTC() {
		t.Errorf("gwell failures shouldn't bump streak or promote — streak=%d forced=%v",
			gwell.TUTKFailStreak(), gwell.ForceWebRTC())
	}

	// Already-WebRTC camera — same story.
	webrtc := newTUTKCam(t, "bell", "GW_BE1")
	for i := 0; i < 10; i++ {
		mgr.recordTUTKFailure(webrtc, "webrtc")
	}
	if webrtc.TUTKFailStreak() != 0 || webrtc.ForceWebRTC() {
		t.Errorf("webrtc failures shouldn't bump streak or promote")
	}
}

func TestRecordTUTKFailure_ThresholdZeroDisables(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.TUTKFallbackThreshold = 0
	cam := newTUTKCam(t, "v4", "HL_CAM4")
	for i := 0; i < 100; i++ {
		mgr.recordTUTKFailure(cam, "tutk")
	}
	if cam.ForceWebRTC() {
		t.Error("threshold=0 should disable auto-fallback")
	}
	if cam.TUTKFailStreak() != 0 {
		t.Errorf("streak = %d, want 0 when disabled", cam.TUTKFailStreak())
	}
}

func TestRecordTUTKFailure_AlreadyPromotedIsNoOp(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.TUTKFallbackThreshold = 3
	cam := newTUTKCam(t, "v4", "HL_CAM4")
	cam.SetForceWebRTC(true)

	var called int
	mgr.OnProtocolFallback(func(string, string, string, int) { called++ })

	for i := 0; i < 20; i++ {
		mgr.recordTUTKFailure(cam, "tutk")
	}
	if called != 0 {
		t.Errorf("fallback callback should not fire when already forced — fired %d times", called)
	}
	if cam.TUTKFailStreak() != 0 {
		t.Errorf("streak should stay 0 on already-promoted camera, got %d", cam.TUTKFailStreak())
	}
}

func TestStreamSourceFor_ForceWebRTCOverridesRegistry(t *testing.T) {
	mgr, _ := newTestManager(t)

	// Baseline: HL_CAM4 defaults to TUTK.
	cam := newTUTKCam(t, "v4", "HL_CAM4")
	if _, p := mgr.streamSourceFor(cam); p != "tutk" {
		t.Fatalf("HL_CAM4 baseline protocol = %q, want tutk", p)
	}
	// Force flag flips the routing decision.
	cam.SetForceWebRTC(true)
	if u, p := mgr.streamSourceFor(cam); p != "webrtc" || u == "" {
		t.Errorf("post-force protocol = %q url = %q, want (webrtc, non-empty)", p, u)
	}
}
