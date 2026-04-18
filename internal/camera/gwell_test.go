package camera

import (
	"context"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// 4.0 architecture: Gwell cameras are handled by the gwell-proxy
// subprocess, which discovers them via /internal/wyze/Camera/* shim
// endpoints on our webui server and RTSP-PUSHes their output straight
// into go2rtc. Their stream slots are pre-declared at startup in the
// go2rtc YAML (empty sources array, publish-only). The camera manager
// does NOT call AddStream for Gwell cameras — it just tracks state.
// When GWELL_ENABLED=false, Gwell cameras are filtered out in
// Discover() so they never enter the registry (and never show up in
// the shim).

func TestManager_GwellCamera_StreamingWhenEnabled(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.GwellEnabled = true

	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "og_cam",
		MAC:   "AABBCCDDEE01",
		Model: "GW_GC1",
	}, "hd", true, false)
	mgr.cameras["og_cam"] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming (gwell-proxy publishes via RTSP PUSH)", cam.GetState())
	}

	// For Gwell cameras the manager must NOT call AddStream — the slot
	// lives in the YAML config. Verify the API wasn't touched.
	streams, _ := go2rtcAPI.ListStreams(context.Background())
	if _, has := streams["og_cam"]; has {
		t.Error("Gwell camera must not be AddStream'd by the manager; slot is pre-declared in YAML")
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
