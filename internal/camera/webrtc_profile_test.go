package camera

import (
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Bulb Cam (HL_BC) WebRTC source URL must carry the #profile= data-
// channel fragment only when WEBRTC_PROFILE is set. See patches/
// (go2rtc PR #2324) for what the fragment unlocks.

func newWebRTCCam(name, model string) *Camera {
	return NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		Model: model,
		MAC:   "AABBCCDD" + strings.ToUpper(name),
	}, "hd", true, false)
}

func TestStreamSourceFor_WebRTCProfileFragment(t *testing.T) {
	cases := []struct {
		name       string
		global     string
		camProfile *string
		wantIn     string
		wantOut    string
	}{
		{"unset leaves upstream URL untouched", "", nil, "#format=wyze", "profile="},
		{"none disables the fragment", "none", nil, "#format=wyze", "profile="},
		{"global 2k appends profile", "2k", nil, "#format=wyze#profile=2k", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := newTestManager(t)
			mgr.cfg.WebrtcProfile = tc.global
			if tc.camProfile != nil {
				mgr.cfg.CamOverrides = map[string]config.CamOverride{
					"BULB": {WebrtcProfile: tc.camProfile},
				}
			}
			cam := newWebRTCCam("bulb", "HL_BC")
			mgr.cameras["bulb"] = cam

			u, proto := mgr.streamSourceFor(cam)
			if proto != "webrtc" {
				t.Fatalf("protocol = %q, want webrtc (HL_BC must be registry WebRTC)", proto)
			}
			if !strings.Contains(u, tc.wantIn) {
				t.Errorf("URL %q missing %q", u, tc.wantIn)
			}
			if tc.wantOut != "" && strings.Contains(u, tc.wantOut) {
				t.Errorf("URL %q should not contain %q", u, tc.wantOut)
			}
		})
	}
}

func TestStreamSourceFor_WebRTCProfilePerCameraOverride(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.WebrtcProfile = "2k"
	off := "none"
	mgr.cfg.CamOverrides = map[string]config.CamOverride{
		"BULB": {WebrtcProfile: &off},
	}
	cam := newWebRTCCam("bulb", "HL_BC")
	mgr.cameras["bulb"] = cam

	u, _ := mgr.streamSourceFor(cam)
	if strings.Contains(u, "profile=") {
		t.Errorf("per-camera 'none' should suppress the fragment, got %q", u)
	}

	// A different camera keeps the global profile.
	other := newWebRTCCam("porch", "HL_BC")
	mgr.cameras["porch"] = other
	u2, _ := mgr.streamSourceFor(other)
	if !strings.Contains(u2, "#profile=2k") {
		t.Errorf("other camera should inherit global 2k, got %q", u2)
	}
}
