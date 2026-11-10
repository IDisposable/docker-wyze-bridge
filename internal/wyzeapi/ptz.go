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
	ptzGetPosition   uint16 = 11006
	ptzPosition      uint16 = 11007
	ptzGetCruise     uint16 = 11010
	ptzCruise        uint16 = 11011
	ptzSetCruise     uint16 = 11012
	ptzSetCruiseAck         = 11013
	ptzSetPosition   uint16 = 11018
	ptzPositionAck          = 11019
	// MaxCruisePoints is the number of numbered positions supported by Pan cameras.
	MaxCruisePoints = 4
)

// CruisePoint is a camera-stored position and its cruise dwell time.
type CruisePoint struct {
	Vertical    int `json:"vertical"`
	Horizontal  int `json:"horizontal"`
	WaitSeconds int `json:"wait_seconds"`
}

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
	return withPTZControl(cam, func(conn *tutkdtls.DTLSConn) error {
		payload := []byte{horizontal, vertical, 5}
		if _, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzMoveAction, payload), matchesPTZMessage(ptzMoveResult), 5*time.Second); err != nil {
			return fmt.Errorf("send PTZ action: %w", err)
		}
		return nil
	})
}

// GetCruisePoints reads the camera's configured cruise positions.
func (c *Client) GetCruisePoints(cam CameraInfo) ([]CruisePoint, error) {
	var points []CruisePoint
	err := withPTZControl(cam, func(conn *tutkdtls.DTLSConn) error {
		message, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzGetCruise, nil), matchesPTZMessage(ptzCruise), 5*time.Second)
		if err != nil {
			return fmt.Errorf("get PTZ cruise points: %w", err)
		}
		payload, err := ptzResponsePayload(message, ptzCruise)
		if err != nil {
			return err
		}
		points, err = parseCruisePoints(payload)
		return err
	})
	return points, err
}

// SaveCruisePoint stores the current camera position in a one-based slot.
// New points must be added sequentially so no configured slots are displaced.
func (c *Client) SaveCruisePoint(cam CameraInfo, slot int) error {
	if slot < 1 || slot > MaxCruisePoints {
		return fmt.Errorf("cruise point must be between 1 and %d", MaxCruisePoints)
	}
	return withPTZControl(cam, func(conn *tutkdtls.DTLSConn) error {
		positionMessage, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzGetPosition, ptzTimestamp()), matchesPTZMessage(ptzPosition), 5*time.Second)
		if err != nil {
			return fmt.Errorf("get current PTZ position: %w", err)
		}
		positionPayload, err := ptzResponsePayload(positionMessage, ptzPosition)
		if err != nil {
			return err
		}
		position, err := parsePTZPosition(positionPayload)
		if err != nil {
			return err
		}

		pointsMessage, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzGetCruise, nil), matchesPTZMessage(ptzCruise), 5*time.Second)
		if err != nil {
			return fmt.Errorf("get PTZ cruise points: %w", err)
		}
		pointsPayload, err := ptzResponsePayload(pointsMessage, ptzCruise)
		if err != nil {
			return err
		}
		points, err := parseCruisePoints(pointsPayload)
		if err != nil {
			return err
		}
		if slot > len(points)+1 {
			return fmt.Errorf("cruise points must be configured sequentially; next available slot is %d", len(points)+1)
		}
		point := CruisePoint{Vertical: position.Vertical, Horizontal: position.Horizontal, WaitSeconds: 10}
		if slot <= len(points) {
			point.WaitSeconds = points[slot-1].WaitSeconds
			points[slot-1] = point
		} else {
			points = append(points, point)
		}
		if err := validateCruisePoints(points); err != nil {
			return err
		}
		if _, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzSetCruise, encodeCruisePoints(points)), matchesPTZMessage(ptzSetCruiseAck), 5*time.Second); err != nil {
			return fmt.Errorf("save PTZ cruise point: %w", err)
		}
		return nil
	})
}

// GoToCruisePoint moves the camera to a one-based configured cruise point.
func (c *Client) GoToCruisePoint(cam CameraInfo, slot int) error {
	if slot < 1 || slot > MaxCruisePoints {
		return fmt.Errorf("cruise point must be between 1 and %d", MaxCruisePoints)
	}
	return withPTZControl(cam, func(conn *tutkdtls.DTLSConn) error {
		message, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzGetCruise, nil), matchesPTZMessage(ptzCruise), 5*time.Second)
		if err != nil {
			return fmt.Errorf("get PTZ cruise points: %w", err)
		}
		payload, err := ptzResponsePayload(message, ptzCruise)
		if err != nil {
			return err
		}
		points, err := parseCruisePoints(payload)
		if err != nil {
			return err
		}
		if slot > len(points) {
			return fmt.Errorf("cruise point %d is not configured", slot)
		}
		if _, err := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzSetPosition, encodePTZPosition(points[slot-1])), matchesPTZMessage(ptzPositionAck), 5*time.Second); err != nil {
			return fmt.Errorf("move to PTZ cruise point %d: %w", slot, err)
		}
		return nil
	})
}

func withPTZControl(cam CameraInfo, action func(*tutkdtls.DTLSConn) error) error {
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
	return action(conn)
}

func ptzTimestamp() []byte {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, uint32(time.Now().Unix()))
	return payload
}

func parsePTZPosition(payload []byte) (CruisePoint, error) {
	if len(payload) != 7 {
		return CruisePoint{}, fmt.Errorf("invalid PTZ position response length %d", len(payload))
	}
	return CruisePoint{
		Vertical:   int(payload[4]),
		Horizontal: int(binary.LittleEndian.Uint16(payload[5:7])),
	}, nil
}

func parseCruisePoints(payload []byte) ([]CruisePoint, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty PTZ cruise points response")
	}
	count := int(payload[0])
	if count > MaxCruisePoints || len(payload) != 1+count*4 {
		return nil, fmt.Errorf("invalid PTZ cruise points response")
	}
	points := make([]CruisePoint, count)
	for i := range points {
		offset := 1 + i*4
		points[i] = CruisePoint{
			Vertical:    int(payload[offset]),
			Horizontal:  int(binary.LittleEndian.Uint16(payload[offset+1 : offset+3])),
			WaitSeconds: int(payload[offset+3]),
		}
	}
	if err := validateCruisePoints(points); err != nil {
		return nil, err
	}
	return points, nil
}

func validateCruisePoints(points []CruisePoint) error {
	if len(points) > MaxCruisePoints {
		return fmt.Errorf("a camera can store at most %d cruise points", MaxCruisePoints)
	}
	for i, point := range points {
		if point.Vertical < 0 || point.Vertical > 40 || point.Horizontal < 0 || point.Horizontal > 350 || point.WaitSeconds < 10 || point.WaitSeconds > 255 {
			return fmt.Errorf("invalid PTZ cruise point %d", i+1)
		}
	}
	return nil
}

func encodeCruisePoints(points []CruisePoint) []byte {
	payload := make([]byte, 1+len(points)*4)
	payload[0] = byte(len(points))
	for i, point := range points {
		offset := 1 + i*4
		payload[offset] = byte(point.Vertical)
		binary.LittleEndian.PutUint16(payload[offset+1:offset+3], uint16(point.Horizontal))
		payload[offset+3] = byte(point.WaitSeconds)
	}
	return payload
}

func encodePTZPosition(point CruisePoint) []byte {
	payload := make([]byte, 7)
	binary.LittleEndian.PutUint32(payload, uint32(time.Now().UnixMilli()%1_000_000_000))
	payload[4] = byte(point.Vertical)
	binary.LittleEndian.PutUint16(payload[5:7], uint16(point.Horizontal))
	return payload
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
	return ptzResponsePayload(message, ptzAuthChallenge)
}

func ptzResponsePayload(message []byte, command uint16) ([]byte, error) {
	if !matchesPTZMessage(command)(message) {
		return nil, fmt.Errorf("invalid TUTK response for command %d", command)
	}
	length := int(binary.LittleEndian.Uint16(message[6:8]))
	if length > len(message)-16 {
		return nil, fmt.Errorf("truncated TUTK response for command %d", command)
	}
	return message[16 : 16+length], nil
}
