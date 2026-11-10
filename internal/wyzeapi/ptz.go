package wyzeapi

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/tutk"
	tutkdtls "github.com/AlexxIT/go2rtc/pkg/tutk/dtls"
)

const (
	ptzAuthRequest   uint16 = 10000
	ptzAuthChallenge        = 10001
	ptzAuthResponse  uint16 = 10002
	ptzAuthResult           = 10003
	ptzMoveAction    uint16 = 11002
	ptzMoveResult           = 11003
)

// ParsePTZDirection maps the public direction names to the TUTK action values.
func ParsePTZDirection(direction string) (horizontal, vertical byte, err error) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "left":
		return 1, 0, nil
	case "right":
		return 2, 0, nil
	case "up":
		return 0, 1, nil
	case "down":
		return 0, 2, nil
	case "stop":
		return 0, 0, nil
	default:
		return 0, 0, fmt.Errorf("invalid PTZ direction %q", direction)
	}
}

// MovePTZ sends a directional action over a short-lived TUTK control connection.
func (c *Client) MovePTZ(cam CameraInfo, direction string) error {
	horizontal, vertical, err := ParsePTZDirection(direction)
	if err != nil {
		return err
	}
	if !cam.SupportsPTZ() {
		return fmt.Errorf("camera model %s does not support TUTK PTZ", cam.Model)
	}
	if net.ParseIP(cam.LanIP) == nil {
		return fmt.Errorf("camera %s has no usable LAN IP for PTZ", cam.Name)
	}
	if cam.P2PID == "" || cam.ENR == "" || cam.MAC == "" {
		return fmt.Errorf("camera %s is missing TUTK credentials for PTZ", cam.Name)
	}

	conn, err := tutkdtls.DialDTLS(cam.LanIP, 0, cam.P2PID, string(tutkdtls.CalculateAuthKey(cam.ENR, cam.MAC)), cam.ENR, false)
	if err != nil {
		return fmt.Errorf("connect for PTZ: %w", err)
	}
	defer conn.Close()

	if err := conn.AVClientStart(5 * time.Second); err != nil {
		return fmt.Errorf("start TUTK control channel: %w", err)
	}
	if err := authenticatePTZ(conn, cam.ENR); err != nil {
		return err
	}

	payload := []byte{horizontal, vertical, 5}
	if _, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzMoveAction, payload), matchesPTZMessage(ptzMoveResult), 5*time.Second); err != nil {
		return fmt.Errorf("send PTZ action: %w", err)
	}
	return nil
}

func authenticatePTZ(conn *tutkdtls.DTLSConn, enr string) error {
	challengeMessage, err := conn.WriteAndWaitIOCtrl(
		encodePTZMessage(ptzAuthRequest, []byte(`{"cameraInfo":{"audioEncoderList":[137,138,140]}}`)),
		matchesPTZMessage(ptzAuthChallenge),
		5*time.Second,
	)
	if err != nil {
		return fmt.Errorf("request TUTK authentication challenge: %w", err)
	}

	challengePayload, err := ptzMessagePayload(challengeMessage)
	if err != nil {
		return err
	}
	if len(challengePayload) < 17 {
		return fmt.Errorf("TUTK authentication challenge is too short")
	}
	challengeResponse, err := ptzChallengeResponse(challengePayload[1:17], enr, challengePayload[0])
	if err != nil {
		return err
	}

	authPayload := make([]byte, 22)
	copy(authPayload, challengeResponse)
	if _, err := rand.Read(authPayload[16:20]); err != nil {
		return fmt.Errorf("create TUTK session id: %w", err)
	}

	if _, err := conn.WriteAndWaitIOCtrl(
		encodePTZMessage(ptzAuthResponse, authPayload),
		matchesPTZMessage(ptzAuthResult),
		5*time.Second,
	); err != nil {
		return fmt.Errorf("authenticate TUTK control channel: %w", err)
	}
	return nil
}

func ptzChallengeResponse(challenge []byte, enr string, status byte) ([]byte, error) {
	var key []byte
	switch status {
	case 1:
		key = []byte("FFFFFFFFFFFFFFFF")
	case 3:
		if len(enr) < 16 {
			return nil, fmt.Errorf("TUTK authentication requires a 16-character ENR")
		}
		key = []byte(enr[:16])
	case 6:
		if len(enr) < 32 {
			return nil, fmt.Errorf("TUTK authentication requires a 32-character ENR")
		}
		challenge = tutk.XXTEADecryptVar(challenge, []byte(enr[:16]))
		key = []byte(enr[16:32])
	default:
		return nil, fmt.Errorf("unsupported TUTK authentication status %d", status)
	}

	response := tutk.XXTEADecryptVar(challenge, key)
	if len(response) < 16 {
		return nil, fmt.Errorf("invalid TUTK authentication challenge")
	}
	return response[:16], nil
}

func encodePTZMessage(command uint16, payload []byte) []byte {
	message := make([]byte, 16+len(payload))
	copy(message, "HL")
	message[2] = 5
	binary.LittleEndian.PutUint16(message[4:6], command)
	binary.LittleEndian.PutUint16(message[6:8], uint16(len(payload)))
	copy(message[16:], payload)
	return message
}

func matchesPTZMessage(command uint16) func([]byte) bool {
	return func(message []byte) bool {
		return len(message) >= 16 &&
			string(message[:2]) == "HL" &&
			binary.LittleEndian.Uint16(message[4:6]) == command
	}
}

func ptzMessagePayload(message []byte) ([]byte, error) {
	if !matchesPTZMessage(ptzAuthChallenge)(message) {
		return nil, fmt.Errorf("invalid TUTK authentication challenge response")
	}
	length := int(binary.LittleEndian.Uint16(message[6:8]))
	if length > len(message)-16 {
		return nil, fmt.Errorf("truncated TUTK authentication challenge response")
	}
	return message[16 : 16+length], nil
}
