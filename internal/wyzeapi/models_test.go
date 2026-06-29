package wyzeapi

import (
	"strings"
	"testing"
)

func TestCameraInfo_NormalizedName(t *testing.T) {
	tests := []struct {
		nickname string
		mac      string
		want     string
	}{
		{"Front Door", "AABBCCDDEEFF", "front_door"},
		{"  Backyard Cam  ", "112233445566", "backyard_cam"},
		{"", "AABBCCDDEEFF", "aabbccddeeff"},
		{"Living Room (Main)", "112233", "living_room_main"},
	}

	for _, tt := range tests {
		cam := CameraInfo{Nickname: tt.nickname, MAC: tt.mac}
		got := cam.NormalizedName()
		if got != tt.want {
			t.Errorf("NormalizedName(%q) = %q, want %q", tt.nickname, got, tt.want)
		}
	}
}

func TestCameraInfo_StreamURL(t *testing.T) {
	cam := CameraInfo{
		LanIP: "192.168.1.10",
		P2PID: "TESTUID123456789012",
		ENR:   "abc123+/=",
		MAC:   "AABBCCDDEEFF",
		Model: "WYZE_CAKP2JFUS",
		DTLS:  true,
	}

	url := cam.StreamURL("hd")

	if !strings.HasPrefix(url, "wyze://192.168.1.10?") {
		t.Errorf("StreamURL should start with wyze://IP, got %q", url)
	}
	if !strings.Contains(url, "uid=TESTUID123456789012") {
		t.Error("StreamURL missing uid")
	}
	if !strings.Contains(url, "mac=AABBCCDDEEFF") {
		t.Error("StreamURL missing mac")
	}
	if !strings.Contains(url, "model=WYZE_CAKP2JFUS") {
		t.Error("StreamURL missing model")
	}
	if !strings.Contains(url, "subtype=hd") {
		t.Error("StreamURL missing subtype")
	}
	if !strings.Contains(url, "dtls=true") {
		t.Error("StreamURL missing dtls")
	}
	// ENR should be URL-encoded
	if !strings.Contains(url, "enr=abc123") {
		t.Error("StreamURL missing enr")
	}
}

func TestCameraInfo_ModelName(t *testing.T) {
	tests := []struct {
		model, want string
	}{
		{"WYZE_CAKP2JFUS", "V3"},
		{"HL_CAM4", "V4"},
		{"WYZEDB3", "Doorbell"},
		{"UNKNOWN_MODEL", "UNKNOWN_MODEL"},
	}

	for _, tt := range tests {
		cam := CameraInfo{Model: tt.model}
		if got := cam.ModelName(); got != tt.want {
			t.Errorf("ModelName(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestCameraInfo_IsGwell(t *testing.T) {
	gwell := CameraInfo{Model: "GW_GC1"}
	if !gwell.IsGwell() {
		t.Error("GW_GC1 should be Gwell")
	}

	normal := CameraInfo{Model: "HL_CAM4"}
	if normal.IsGwell() {
		t.Error("HL_CAM4 should not be Gwell")
	}
}

func TestCameraInfo_IsGwellP2P(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"GW_GC1", true},
		{"GW_GC2", true},
		{"GW_BE1", false},
		{"GW_DBD", false},
		{"HL_CAM4", false},
	}
	for _, tt := range tests {
		cam := CameraInfo{Model: tt.model}
		if got := cam.IsGwellP2P(); got != tt.want {
			t.Errorf("IsGwellP2P(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestCameraInfo_IsWebRTCStreamer(t *testing.T) {
	tests := []struct {
		name  string
		model string
		ip    string
		want  bool
	}{
		{"doorbell pro always webrtc", "GW_BE1", "", true},
		{"doorbell duo always webrtc", "GW_DBD", "10.0.0.1", true},
		{"OG with LAN IP is gwell p2p", "GW_GC1", "10.0.0.7", false},
		{"OG with empty IP is still gwell p2p", "GW_GC1", "", false},
		{"OG 3X with empty IP is still gwell p2p", "GW_GC2", "", false},
		{"OG with 0.0.0.0 is still gwell p2p", "GW_GC1", "0.0.0.0", false},
		{"TUTK camera is not webrtc", "HL_CAM4", "10.0.0.5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cam := CameraInfo{Model: tt.model, LanIP: tt.ip}
			if got := cam.IsWebRTCStreamer(); got != tt.want {
				t.Errorf("IsWebRTCStreamer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCameraInfo_IsPanCam(t *testing.T) {
	pan := CameraInfo{Model: "HL_PAN3"}
	if !pan.IsPanCam() {
		t.Error("HL_PAN3 should be a pan cam")
	}

	notPan := CameraInfo{Model: "HL_CAM4"}
	if notPan.IsPanCam() {
		t.Error("HL_CAM4 should not be a pan cam")
	}
}

func TestCameraInfo_IsDoorbell(t *testing.T) {
	db := CameraInfo{Model: "WYZEDB3"}
	if !db.IsDoorbell() {
		t.Error("WYZEDB3 should be a doorbell")
	}

	notDB := CameraInfo{Model: "HL_CAM4"}
	if notDB.IsDoorbell() {
		t.Error("HL_CAM4 should not be a doorbell")
	}
}
