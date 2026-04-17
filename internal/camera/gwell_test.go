package camera

import (
	"context"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// All stream registration goes through go2rtc's HTTP API (AddStream).
// Source URL depends on protocol:
//   - TUTK: wyze:// — go2rtc dials the camera directly.
//   - OG Gwell (LAN-direct): empty URL → publish-only slot that
//     gwell-proxy RTSP-PUSHes into.
//   - WebRTC (GW_BE1 / GW_DBD / no-LAN-IP Gwell): webrtc:…#format=wyze
//     — go2rtc's native handler runs the KVS signaling dance itself.
// When GWELL_ENABLED=false, Gwell cameras are filtered out in Discover
// so they never enter the registry.

func TestManager_GwellOGCamera_RegistersPublishOnlySlot(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.GwellEnabled = true

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_cam",
		MAC:   "AABBCCDDEE01",
		Model: "GW_GC1",
		LanIP: "10.0.0.7", // has a LAN IP → not a WebRTC streamer
	}, "hd", true, false)
	mgr.cameras["og_cam"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming (slot registered, awaiting gwell-proxy publish)", cam.GetState())
	}

	// OG camera slot is registered via AddStream with an empty source
	// URL so gwell-proxy's ffmpeg RTSP PUBLISH has a named target.
	streams, _ := go2rtcAPI.ListStreams(context.Background())
	if _, has := streams["og_cam"]; !has {
		t.Error("OG Gwell camera should have been registered via AddStream (publish-only slot)")
	}
}

func TestManager_GwellWebRTCCamera_UsesWyzeFormatSource(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.GwellEnabled = true

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "front_door",
		MAC:   "AABBCCDDEE02",
		Model: "GW_BE1", // doorbell lineage, WebRTC
	}, "hd", true, false)
	mgr.cameras["front_door"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}
	streams, _ := go2rtcAPI.ListStreams(context.Background())
	if _, has := streams["front_door"]; !has {
		t.Error("WebRTC Gwell camera should have been registered via AddStream")
	}
}

func TestManager_TUTKCamera_UsesAddStream(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.GwellEnabled = true // shouldn't matter for TUTK path

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "v3_cam",
		LanIP: "10.0.0.5",
		P2PID: "UID",
		ENR:   "enr",
		MAC:   "AA",
		Model: "WYZE_CAKP2JFUS",
		DTLS:  true,
	}, "hd", true, false)
	mgr.cameras["v3_cam"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}
	streams, _ := go2rtcAPI.ListStreams(context.Background())
	if _, has := streams["v3_cam"]; !has {
		t.Error("TUTK camera should have been AddStream'd to go2rtc")
	}
}

func TestManager_Discover_GwellFilter(t *testing.T) {
	// The filter loop inside Discover() — tested here against the
	// cfg.GwellEnabled flag directly, without spinning a live Wyze
	// API mock.
	cameras := []wyzeapi.CameraInfo{
		{Nickname: "V3", Model: "WYZE_CAKP2JFUS", MAC: "A1"},
		{Nickname: "OG", Model: "GW_GC1", MAC: "A2"},
		{Nickname: "Door", Model: "GW_BE1", MAC: "A3"},
	}

	t.Run("disabled: Gwell filtered", func(t *testing.T) {
		cfg := &config.Config{GwellEnabled: false}
		if got := applyGwellFilter(cfg, cameras); len(got) != 1 || got[0].Model != "WYZE_CAKP2JFUS" {
			t.Errorf("got %+v, want only TUTK", got)
		}
	})

	t.Run("enabled: Gwell included", func(t *testing.T) {
		cfg := &config.Config{GwellEnabled: true}
		if got := applyGwellFilter(cfg, cameras); len(got) != 3 {
			t.Errorf("want all 3, got %d: %+v", len(got), got)
		}
	})
}

// applyGwellFilter mirrors the filter loop in Manager.Discover so we
// can unit-test the decision without a live HTTP Wyze API. Keep in
// sync with manager.go's Discover.
func applyGwellFilter(cfg *config.Config, cameras []wyzeapi.CameraInfo) []wyzeapi.CameraInfo {
	var supported []wyzeapi.CameraInfo
	for _, cam := range cameras {
		if cam.IsGwell() && !cfg.GwellEnabled {
			continue
		}
		supported = append(supported, cam)
	}
	return supported
}
