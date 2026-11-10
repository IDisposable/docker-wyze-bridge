package camera

import (
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestStatusJSONSupportsPTZFollowsProtocol(t *testing.T) {
	cam := NewCamera(wyzeapi.CameraInfo{
		Name:  "pan",
		Model: "HL_PAN3",
		DTLS:  true,
	}, "hd", true, false)

	if got := cam.StatusJSON()["supports_ptz"]; got != true {
		t.Errorf("supports_ptz = %v, want true", got)
	}

	cam.SetForceWebRTC(true)
	if got := cam.StatusJSON()["supports_ptz"]; got != false {
		t.Errorf("supports_ptz after WebRTC fallback = %v, want false", got)
	}
}
