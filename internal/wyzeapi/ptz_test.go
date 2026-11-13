package wyzeapi

import (
	"encoding/binary"
	"errors"
	"fmt"
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

func TestDialPTZWithDiscoveryRetry(t *testing.T) {
	t.Run("retries discovery once", func(t *testing.T) {
		attempts := 0
		conn, err := dialPTZWithDiscoveryRetry(func() (ptzConn, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("discovery timeout")
			}
			return nil, nil
		}, true)
		if err != nil || conn != nil {
			t.Fatalf("dial result = (%v, %v), want (nil, nil)", conn, err)
		}
		if attempts != 2 {
			t.Fatalf("dial attempts = %d, want 2", attempts)
		}
	})

	t.Run("does not retry other failures", func(t *testing.T) {
		attempts := 0
		_, err := dialPTZWithDiscoveryRetry(func() (ptzConn, error) {
			attempts++
			return nil, errors.New("connection refused")
		}, true)
		if err == nil || attempts != 1 {
			t.Fatalf("dial attempts = %d, error = %v; want one attempt and an error", attempts, err)
		}
	})

	t.Run("retry is opt-in", func(t *testing.T) {
		attempts := 0
		_, err := dialPTZWithDiscoveryRetry(func() (ptzConn, error) {
			attempts++
			return nil, errors.New("discovery timeout")
		}, false)
		if err == nil || attempts != 1 {
			t.Fatalf("dial attempts = %d, error = %v; want one attempt and an error", attempts, err)
		}
	})

	t.Run("retry is bounded", func(t *testing.T) {
		attempts := 0
		_, err := dialPTZWithDiscoveryRetry(func() (ptzConn, error) {
			attempts++
			return nil, errors.New("discovery timeout")
		}, true)
		if err == nil || attempts != ptzStopDiscoveryRetries+1 {
			t.Fatalf("dial attempts = %d, error = %v; want %d attempts and an error", attempts, err, ptzStopDiscoveryRetries+1)
		}
	})
}

func TestPTZDeliveryForError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want PTZDelivery
	}{
		{
			name: "failure before action send",
			err:  fmt.Errorf("connect: %w", &PTZError{Delivery: PTZDeliveryNotSent, Err: errors.New("discovery timeout")}),
			want: PTZDeliveryNotSent,
		},
		{
			name: "failure after possible send",
			err:  &PTZError{Delivery: PTZDeliveryUnknown, Err: errors.New("ack timeout")},
			want: PTZDeliveryUnknown,
		},
		{
			name: "untyped error is conservative",
			err:  errors.New("unknown failure"),
			want: PTZDeliveryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PTZDeliveryForError(tt.err); got != tt.want {
				t.Fatalf("PTZDeliveryForError() = %q, want %q", got, tt.want)
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

func TestPTZResponseMatchesWrappedIOCtrl(t *testing.T) {
	tests := []struct {
		name   string
		offset int
	}{
		{name: "channel message", offset: 36},
		{name: "ioctl message", offset: 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte{1, 2, 3}
			hlMessage := encodePTZMessage(ptzAuthChallenge, payload)
			wrapped := make([]byte, tt.offset+len(hlMessage))
			copy(wrapped[tt.offset:], hlMessage)

			if !matchesPTZMessage(ptzAuthChallenge)(wrapped) {
				t.Fatal("wrapped K10001 response did not match")
			}
			got, err := ptzMessagePayload(wrapped)
			if err != nil {
				t.Fatalf("ptzMessagePayload() error = %v", err)
			}
			if string(got) != string(payload) {
				t.Errorf("payload = %v, want %v", got, payload)
			}
		})
	}
}

func TestCruisePointProtocolEncoding(t *testing.T) {
	points := []CruisePoint{
		{Vertical: 8, Horizontal: 120, WaitSeconds: 10},
		{Vertical: 40, Horizontal: 350, WaitSeconds: 255},
	}
	payload := encodeCruisePoints(points)
	want := []byte{2, 8, 120, 0, 10, 40, 94, 1, 255}
	if string(payload) != string(want) {
		t.Fatalf("encodeCruisePoints() = %v, want %v", payload, want)
	}

	got, err := parseCruisePoints(payload)
	if err != nil {
		t.Fatalf("parseCruisePoints() error = %v", err)
	}
	if len(got) != len(points) || got[0] != points[0] || got[1] != points[1] {
		t.Errorf("parseCruisePoints() = %+v, want %+v", got, points)
	}
}

func TestParseCruisePointsRejectsInvalidResponses(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		{1, 8, 120},
		{5},
	} {
		if _, err := parseCruisePoints(payload); err == nil {
			t.Errorf("parseCruisePoints(%v) unexpectedly succeeded", payload)
		}
	}
}

func TestParseCruisePointsPreservesCameraValues(t *testing.T) {
	payload := []byte{1, 41, 0x5f, 0x01, 0}
	points, err := parseCruisePoints(payload)
	if err != nil {
		t.Fatalf("parseCruisePoints() error = %v", err)
	}
	want := []CruisePoint{{Vertical: 41, Horizontal: 351, WaitSeconds: 0}}
	if len(points) != len(want) || points[0] != want[0] {
		t.Errorf("parseCruisePoints() = %+v, want %+v", points, want)
	}
}

func TestParsePTZPosition(t *testing.T) {
	position, err := parsePTZPosition([]byte{1, 0, 0, 0, 8, 120, 0})
	if err != nil {
		t.Fatalf("parsePTZPosition() error = %v", err)
	}
	if position.Vertical != 8 || position.Horizontal != 120 {
		t.Errorf("position = %+v, want vertical 8 horizontal 120", position)
	}
}
