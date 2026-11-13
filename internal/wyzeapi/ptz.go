package wyzeapi

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
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

	ptzStopDiscoveryRetries    = 1
	ptzStopDiscoveryRetryDelay = 200 * time.Millisecond
)

var ptzDiagnosticSequence atomic.Uint64

type PTZDelivery string

const (
	PTZDeliveryAcknowledged PTZDelivery = "acknowledged"
	PTZDeliveryNotSent      PTZDelivery = "not_sent"
	PTZDeliveryUnknown      PTZDelivery = "unknown"
)

// PTZError records whether a PTZ action was sent when an operation failed.
type PTZError struct {
	Delivery PTZDelivery
	Err      error
}

func (e *PTZError) Error() string {
	return e.Err.Error()
}

func (e *PTZError) Unwrap() error {
	return e.Err
}

// PTZDeliveryForError reports not_sent only when the failure occurred before
// the requested PTZ action could be written. Other errors are conservatively
// classified as unknown.
func PTZDeliveryForError(err error) PTZDelivery {
	var ptzErr *PTZError
	if errors.As(err, &ptzErr) {
		return ptzErr.Delivery
	}
	return PTZDeliveryUnknown
}

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
	return c.MovePTZAttempt(cam, direction, "")
}

// MovePTZAttempt is MovePTZ with an explicit attempt id for the opt-in transport
// trace (PTZ_TRACE). Callers that want to tie one Home Assistant or WebUI press to
// its stage records pass their own id; ids never contain camera identifiers.
func (c *Client) MovePTZAttempt(cam CameraInfo, direction, attemptID string) error {
	horizontal, vertical, err := ParsePTZDirection(direction)
	if err != nil {
		return &PTZError{Delivery: PTZDeliveryNotSent, Err: err}
	}
	trace := newPTZTrace(cam, direction, attemptID)
	// STOP is the recovery action, so it gets the bounded discovery retry;
	// movements never do (see ptzStopDiscoveryRetries).
	retryDiscovery := strings.EqualFold(strings.TrimSpace(direction), "stop")
	err = withPTZControlOptions(cam, retryDiscovery, trace, func(conn ptzConn) error {
		payload := []byte{horizontal, vertical, 5}
		started := time.Now()
		ack, writeErr := conn.WriteAndWaitIOCtrl(encodePTZMessage(ptzMoveAction, payload), matchesPTZMessage(ptzMoveResult), 5*time.Second)
		if writeErr != nil {
			// A response timeout still means the action frame was written:
			// go2rtc retransmits it while waiting. So the write stage stays ok
			// and only the ack stage records the miss. This is the datum that
			// separates "camera never got the STOP" from "camera acked it".
			trace.stage(PTZStageActionWrite, isPTZResponseTimeout(writeErr), ptzActionDetail(writeErr), started)
			trace.ack(PTZStageAckReceived, false, PTZDetailAbsent, classifyPTZAck(ack, ptzMoveResult), ack, started)
			return fmt.Errorf("send PTZ action: %w", writeErr)
		}
		trace.stage(PTZStageActionWrite, true, PTZDetailOK, started)
		trace.ack(PTZStageAckReceived, true, PTZDetailOK, classifyPTZAck(ack, ptzMoveResult), ack, started)
		return nil
	})
	trace.result(err)
	return err
}

// GetCruisePoints reads the camera's configured cruise positions.
func (c *Client) GetCruisePoints(cam CameraInfo) ([]CruisePoint, error) {
	var points []CruisePoint
	err := withPTZControl(cam, func(conn ptzConn) error {
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
	return withPTZControl(cam, func(conn ptzConn) error {
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
			if wait := points[slot-1].WaitSeconds; wait >= 10 {
				point.WaitSeconds = wait
			}
			points[slot-1] = point
		} else {
			points = append(points, point)
		}
		if err := validateCruisePoints([]CruisePoint{point}); err != nil {
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
	return withPTZControl(cam, func(conn ptzConn) error {
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

// ptzConn is the transport surface the PTZ protocol needs from a control
// connection. Production uses the pinned go2rtc DTLS connection; tests inject a
// fake through ptzDial so every stage (P2P discovery, session start, auth,
// action write, ack) can be driven without a camera on the network.
type ptzConn interface {
	AVClientStart(timeout time.Duration) error
	WriteAndWaitIOCtrl(payload []byte, match func([]byte) bool, timeout time.Duration) ([]byte, error)
	Close() error
}

// ptzDial opens one TUTK control connection. diagnosticID comes from the
// PTZ_TUTK_DISCOVERY_DEBUG opt-in and only selects the go2rtc dialer that logs
// discovery attempts; it never carries credentials.
var ptzDial = func(cam CameraInfo, diagnosticID string) (ptzConn, error) {
	authKey := string(tutkdtls.CalculateAuthKey(cam.ENR, cam.MAC))
	if diagnosticID != "" {
		return tutkdtls.DialDTLSWithDiagnostics(cam.LanIP, 0, cam.P2PID, authKey, cam.ENR, false, diagnosticID)
	}
	return tutkdtls.DialDTLS(cam.LanIP, 0, cam.P2PID, authKey, cam.ENR, false)
}

func withPTZControl(cam CameraInfo, action func(conn ptzConn) error) error {
	return withPTZControlOptions(cam, false, nil, action)
}

// withPTZControlOptions opens a control connection, authenticates it, runs the
// action, and closes it. trace may be nil; when enabled it records each stage so
// a STOP can be told apart from a STOP that reached the camera but had no effect.
// The connection is always short-lived and go2rtc sends no AV logout, so every
// attempt takes a camera-side session slot that only the camera's own timeout
// frees.
func withPTZControlOptions(cam CameraInfo, retryDiscovery bool, trace *ptzTrace, action func(conn ptzConn) error) error {
	if !cam.SupportsPTZ() {
		trace.gate(PTZDetailUnsupportedModel)
		return &PTZError{Delivery: PTZDeliveryNotSent, Err: fmt.Errorf("camera model %s does not support TUTK PTZ", cam.Model)}
	}
	if net.ParseIP(cam.LanIP) == nil {
		trace.gate(PTZDetailNoLANIP)
		return &PTZError{Delivery: PTZDeliveryNotSent, Err: fmt.Errorf("camera %s has no usable LAN IP for PTZ", cam.Name)}
	}
	if cam.P2PID == "" || cam.ENR == "" || cam.MAC == "" {
		trace.gate(PTZDetailMissingCredentials)
		return &PTZError{Delivery: PTZDeliveryNotSent, Err: fmt.Errorf("camera %s is missing TUTK credentials for PTZ", cam.Name)}
	}

	debug := os.Getenv("PTZ_TUTK_DISCOVERY_DEBUG") == "true"
	diagnosticID := ""
	withDiagnosticID := func(err error) error {
		if err != nil && diagnosticID != "" {
			return fmt.Errorf("PTZ diagnostic_id=%s: %w", diagnosticID, err)
		}
		return err
	}

	// Each attempt is a fresh P2P discovery + DTLS handshake + AV login, so a
	// dial failure is classified per attempt: "discovery timeout" means the
	// camera never answered hole-punching, anything else is a transport error.
	operationID := ""
	if debug {
		operationID = fmt.Sprintf("ptz-%d", ptzDiagnosticSequence.Add(1))
	}
	attempt := 0
	dialStart := time.Now()
	conn, err := dialPTZWithDiscoveryRetry(func() (ptzConn, error) {
		attempt++
		if trace != nil {
			trace.attemptNo = attempt
		}
		if debug {
			diagnosticID = operationID
			if attempt > 1 {
				diagnosticID = fmt.Sprintf("%s-retry-%d", operationID, attempt-1)
			}
		}
		start := time.Now()
		conn, err := ptzDial(cam, diagnosticID)
		trace.stage(PTZStageP2PConnection, err == nil, ptzDialDetail(err), start)
		return conn, err
	}, retryDiscovery)
	if err != nil {
		trace.stage(PTZStageDTLSSession, false, PTZDetailNotAttempted, dialStart)
		return &PTZError{
			Delivery: PTZDeliveryNotSent,
			Err:      withDiagnosticID(fmt.Errorf("connect for PTZ: %w", err)),
		}
	}
	defer conn.Close()
	if err := conn.AVClientStart(5 * time.Second); err != nil {
		trace.stage(PTZStageDTLSSession, false, PTZDetailChannelStart, dialStart)
		return &PTZError{
			Delivery: PTZDeliveryNotSent,
			Err:      withDiagnosticID(fmt.Errorf("start TUTK control channel: %w", err)),
		}
	}
	trace.stage(PTZStageDTLSSession, true, PTZDetailOK, dialStart)
	if err := authenticatePTZ(conn, cam.ENR); err != nil {
		trace.stage(PTZStageAuthExchange, false, ptzAuthDetail(err), dialStart)
		return &PTZError{Delivery: PTZDeliveryNotSent, Err: withDiagnosticID(err)}
	}
	trace.stage(PTZStageAuthExchange, true, PTZDetailOK, dialStart)
	if err := action(conn); err != nil {
		return &PTZError{Delivery: PTZDeliveryUnknown, Err: withDiagnosticID(err)}
	}
	return nil
}

func dialPTZWithDiscoveryRetry(dial func() (ptzConn, error), retryDiscovery bool) (ptzConn, error) {
	for attempt := 0; ; attempt++ {
		conn, err := dial()
		if err == nil || !retryDiscovery || attempt >= ptzStopDiscoveryRetries || !isPTZDiscoveryTimeout(err) {
			return conn, err
		}
		time.Sleep(ptzStopDiscoveryRetryDelay)
	}
}

func isPTZDiscoveryTimeout(err error) bool {
	// Pinned go2rtc v1.9.14 returns this plain error before the PTZ action can be sent.
	return err != nil && err.Error() == "discovery timeout"
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

func authenticatePTZ(conn ptzConn, enr string) error {
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
		message = findPTZMessage(message)
		return len(message) >= 16 &&
			binary.LittleEndian.Uint16(message[4:6]) == command
	}
}

func findPTZMessage(message []byte) []byte {
	if len(message) >= 16 && string(message[:2]) == "HL" {
		return message
	}
	for i := 32; i+16 <= len(message); i++ {
		if message[i] == 'H' && message[i+1] == 'L' {
			return message[i:]
		}
	}
	return nil
}

func ptzMessagePayload(message []byte) ([]byte, error) {
	return ptzResponsePayload(message, ptzAuthChallenge)
}

func ptzResponsePayload(message []byte, command uint16) ([]byte, error) {
	message = findPTZMessage(message)
	if !matchesPTZMessage(command)(message) {
		return nil, fmt.Errorf("invalid TUTK response for command %d", command)
	}
	length := int(binary.LittleEndian.Uint16(message[6:8]))
	if length > len(message)-16 {
		return nil, fmt.Errorf("truncated TUTK response for command %d", command)
	}
	return message[16 : 16+length], nil
}
