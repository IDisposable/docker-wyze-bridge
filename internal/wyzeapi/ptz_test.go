package wyzeapi

import (
	"encoding/binary"
	"testing"
)

func TestParsePTZDirection(t *testing.T) {
	tests := []struct {
		direction  string
		horizontal byte
		vertical   byte
		wantErr    bool
	}{
		{"left", 1, 0, false},
		{"right", 2, 0, false},
		{"up", 0, 1, false},
		{"down", 0, 2, false},
		{"stop", 0, 0, false},
		{" LEFT ", 1, 0, false},
		{"diagonal", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.direction, func(t *testing.T) {
			horizontal, vertical, err := ParsePTZDirection(tt.direction)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePTZDirection() error = %v, wantErr %v", err, tt.wantErr)
			}
			if horizontal != tt.horizontal || vertical != tt.vertical {
				t.Errorf("ParsePTZDirection() = (%d, %d), want (%d, %d)", horizontal, vertical, tt.horizontal, tt.vertical)
			}
		})
	}
}

func TestCameraInfoSupportsPTZ(t *testing.T) {
	tests := []struct {
		name string
		cam  CameraInfo
		want bool
	}{
		{"Pan V1 DTLS", CameraInfo{Model: "WYZECP1_JEF", DTLS: true}, true},
		{"Pan V2 DTLS", CameraInfo{Model: "HL_PAN2", DTLS: true}, true},
		{"Pan V3 DTLS", CameraInfo{Model: "HL_PAN3", DTLS: true}, true},
		{"Pan Pro DTLS", CameraInfo{Model: "HL_PANP", DTLS: true}, true},
		{"Pan without DTLS", CameraInfo{Model: "HL_PAN3"}, false},
		{"non-Pan DTLS", CameraInfo{Model: "WYZE_CAKP2JFUS", DTLS: true}, false},
		{"WebRTC Pan", CameraInfo{Model: "GW_DUO", DTLS: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cam.SupportsPTZ(); got != tt.want {
				t.Errorf("SupportsPTZ() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEncodePTZMessage(t *testing.T) {
	message := encodePTZMessage(ptzMoveAction, []byte{1, 0, 5})
	if len(message) != 19 || string(message[:2]) != "HL" || message[2] != 5 {
		t.Fatalf("unexpected PTZ message header: %v", message[:16])
	}
	if command := binary.LittleEndian.Uint16(message[4:6]); command != ptzMoveAction {
		t.Errorf("command = %d, want %d", command, ptzMoveAction)
	}
	if length := binary.LittleEndian.Uint16(message[6:8]); length != 3 {
		t.Errorf("payload length = %d, want 3", length)
	}
	if got := message[16:]; string(got) != string([]byte{1, 0, 5}) {
		t.Errorf("payload = %v, want [1 0 5]", got)
	}
}
