package wyzeapi

// Opt-in, sanitized PTZ transport trace.
//
// A Home Assistant button (or the WebUI pad) reports what the bridge managed to
// do, not what the camera physically did. When a user says "move worked but STOP
// did not", the only way to tell a transport failure (camera unreachable,
// session slot gone) from a protocol refusal (camera acknowledged K11002 but did
// not act) is to record each stage of one control attempt.
//
// Set PTZ_TRACE to a file path, or to stdout/console/1/on/true, to enable it.
// Records are JSON lines. The trace NEVER contains identifiers or credentials:
// no P2PID/UID, no ENR, no auth key, no LAN IP, no raw payload bytes. It carries
// stage names, elapsed time, ack classification, and ack length only. It is a
// diagnostic log, not a decision source: delivery reporting still comes from the
// protocol result, and STOP retry behaviour is unchanged by it.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Stages of one PTZ control attempt, in order. Home Assistant helpers, docs, and
// tests refer to these by name, so treat renaming as breaking.
const (
	PTZStageCapabilityGate = "capability_gate"
	PTZStageP2PConnection  = "p2p_connection"
	PTZStageDTLSSession    = "dtls_session"
	PTZStageAuthExchange   = "auth_exchange"
	PTZStageActionWrite    = "action_write"
	PTZStageAckReceived    = "ack_received"
	PTZStageResult         = "result"
)

// Stage detail classifications. Kept as fixed strings so the WebUI and docs can
// explain a stage without parsing free-form error text.
const (
	PTZDetailOK                 = "ok"
	PTZDetailAttemptTimeout     = "attempted_timeout"
	PTZDetailConnectionError    = "connection_error"
	PTZDetailNotAttempted       = "not_attempted"
	PTZDetailChannelStart       = "channel_start_failed"
	PTZDetailAuthChallenge      = "challenge_failed"
	PTZDetailAuthShort          = "challenge_short"
	PTZDetailAuthRejected       = "auth_rejected"
	PTZDetailWriteFailed        = "write_failed"
	PTZDetailAckTimeout         = "ack_timeout"
	PTZDetailAbsent             = "absent"
	PTZDetailUnsupportedModel   = "unsupported_model"
	PTZDetailNoLANIP            = "no_lan_ip"
	PTZDetailMissingCredentials = "missing_tutk_credentials"
	PTZResultNotSent            = "not_sent"
	PTZResultUnknownDelivery    = "unknown_delivery"
	PTZResultFailed             = "failed"
)

// Ack classifications. `ack_len` is the length of what the camera sent back:
// "missing" means nothing matched the response filter before the timeout,
// "empty" means the reply carried a zero-length payload (the action was taken
// with no return value), "echo_ok" means it came back with a payload.
const (
	PTZAckEchoOK     = "echo_ok"
	PTZAckMissing    = "missing"
	PTZAckEmpty      = "empty"
	PTZAckMalformed  = "malformed"
	PTZAckUnexpected = "unexpected_command"
	PTZAckTruncated  = "truncated"
)

// PTZTraceRecord is one line of the trace, and the shape returned to the WebUI.
type PTZTraceRecord struct {
	TS        string `json:"ts"`
	Attempt   string `json:"attempt"`
	Camera    string `json:"camera"`
	Command   string `json:"command"`
	Stage     string `json:"stage"`
	AttemptNo int    `json:"attempt_no,omitempty"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
	AckClass  string `json:"ack_class,omitempty"`
	AckLen    int    `json:"ack_len,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
}

// ptzTraceEnv values that route records to stdout instead of a file.
var ptzTraceStdout = map[string]bool{"1": true, "on": true, "true": true, "yes": true, "stdout": true, "console": true, "-": true}

var ptzTraceSequence atomic.Uint64

// PTZTraceAttemptID returns an opaque attempt id safe to surface in Home
// Assistant. It never contains camera identifiers.
func PTZTraceAttemptID(prefix string) string {
	if prefix == "" {
		prefix = "ptz"
	}
	return fmt.Sprintf("%s-%d-%04d", prefix, time.Now().Unix(), ptzTraceSequence.Add(1)%10000)
}

// ptzTraceSink resolves where records go. ok=false means tracing is off.
func ptzTraceSink() (path string, stdout bool, ok bool) {
	value := strings.TrimSpace(os.Getenv("PTZ_TRACE"))
	if value == "" || strings.EqualFold(value, "false") || strings.EqualFold(value, "off") || value == "0" {
		return "", false, false
	}
	if ptzTraceStdout[strings.ToLower(value)] {
		return "", true, true
	}
	return value, false, true
}

// PTZTraceEnabled reports whether the transport trace is on.
func PTZTraceEnabled() bool {
	_, _, ok := ptzTraceSink()
	return ok
}

// ptzTrace emits one attempt's records. A nil trace is valid: the methods below
// no-op, so the transport path stays readable.
type ptzTrace struct {
	attempt   string
	command   string
	camera    string
	attemptNo int
	started   time.Time
}

// newPTZTrace starts one attempt. The sanitized stage history is kept even when
// no PTZ_TRACE sink is configured, so the WebUI can always show what happened to
// the last movement or STOP; the sink only decides whether records are also
// written to a file or to the log.
func newPTZTrace(cam CameraInfo, direction, attemptID string) *ptzTrace {
	if attemptID == "" {
		attemptID = PTZTraceAttemptID("ptz")
	}
	tr := &ptzTrace{attempt: attemptID, camera: cam.Name, started: time.Now()}
	if _, _, err := ParsePTZDirection(direction); err == nil {
		tr.command = ptzCommandLabel(ptzMoveAction)
	}
	return tr
}

func ptzCommandLabel(command uint16) string {
	return fmt.Sprintf("K%d", command)
}

// gate records a rejection before any connection was attempted (wrong model, no
// LAN IP, missing TUTK credentials). The attempt then ends with a result record.
func (t *ptzTrace) gate(detail string) {
	if t == nil {
		return
	}
	t.emit(PTZTraceRecord{Stage: PTZStageCapabilityGate, OK: false, Detail: detail})
}

// result records how the attempt ended overall.
func (t *ptzTrace) result(err error) {
	if t == nil {
		return
	}
	t.emit(PTZTraceRecord{
		Stage:     PTZStageResult,
		OK:        err == nil,
		Detail:    ptzResultDetail(err),
		AttemptNo: t.attemptNo,
		ElapsedMS: time.Since(t.started).Milliseconds(),
	})
}

// ptzResultDetail maps a transport error to the delivery vocabulary used by
// command/status, so a trace line and a Home Assistant status line agree.
func ptzResultDetail(err error) string {
	if err == nil {
		return PTZDetailOK
	}
	var pe *PTZError
	if errors.As(err, &pe) {
		switch pe.Delivery {
		case PTZDeliveryNotSent:
			return PTZResultNotSent
		case PTZDeliveryUnknown:
			return PTZResultUnknownDelivery
		}
	}
	return PTZResultFailed
}

// stage writes a stage record with its outcome and timing.
func (t *ptzTrace) stage(stage string, ok bool, detail string, started time.Time) {
	if t == nil {
		return
	}
	t.emit(PTZTraceRecord{
		Stage:     stage,
		OK:        ok,
		Detail:    detail,
		AttemptNo: t.attemptNo,
		ElapsedMS: time.Since(started).Milliseconds(),
	})
}

func (t *ptzTrace) ack(stage string, ok bool, detail, class string, ack []byte, started time.Time) {
	if t == nil {
		return
	}
	t.emit(PTZTraceRecord{
		Stage:     stage,
		OK:        ok,
		Detail:    detail,
		AckClass:  class,
		AckLen:    len(ack),
		AttemptNo: t.attemptNo,
		ElapsedMS: time.Since(started).Milliseconds(),
	})
}

func (t *ptzTrace) emit(rec PTZTraceRecord) {
	if t == nil {
		return
	}
	rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	rec.Attempt = t.attempt
	rec.Camera = t.camera
	rec.Command = t.command
	if rec.Detail == "" && rec.OK {
		rec.Detail = PTZDetailOK
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	recordPTZTrace(t.camera, rec)
	path, stdout, ok := ptzTraceSink()
	if !ok {
		return
	}
	if stdout {
		event := log.Info().Str("component", "ptz_trace").Str("attempt", rec.Attempt).
			Str("stage", rec.Stage).Bool("ok", rec.OK)
		if rec.Detail != "" {
			event = event.Str("detail", rec.Detail)
		}
		if rec.AckClass != "" {
			event = event.Str("ack_class", rec.AckClass)
		}
		if rec.AckLen > 0 {
			event = event.Int("ack_len", rec.AckLen)
		}
		if rec.ElapsedMS > 0 {
			event = event.Int64("elapsed_ms", rec.ElapsedMS)
		}
		event.Msg("PTZ transport stage")
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Warn().Err(err).Str("component", "ptz_trace").Msg("could not open PTZ trace file")
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// classifyPTZAck describes what the camera sent back, without exposing bytes.
// It answers the question a STOP report hinges on: did the camera answer the
// action at all, and did that answer carry anything?
func classifyPTZAck(ack []byte, command uint16) string {
	if len(ack) == 0 {
		return PTZAckMissing
	}
	message := findPTZMessage(ack)
	if message == nil || len(message) < 16 {
		return PTZAckMalformed
	}
	if got := binary.LittleEndian.Uint16(message[4:6]); got != command {
		return PTZAckUnexpected
	}
	declared := int(binary.LittleEndian.Uint16(message[6:8]))
	if declared > len(message)-16 {
		return PTZAckTruncated
	}
	if declared == 0 {
		return PTZAckEmpty
	}
	return PTZAckEchoOK
}

// isPTZResponseTimeout reports go2rtc's "the reply never matched" error. go2rtc
// has already written the action frame and retransmits it once a second while it
// waits, so a response timeout still means the frame reached the wire; that is
// what separates "the camera never answered" from "we never sent it".
func isPTZResponseTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout waiting for response")
}

// ptzDialDetail classifies a dial failure well enough to point at a cause:
// "discovery timeout" is a P2P hole-punch/NAT failure (camera never answered
// discovery), anything else is a generic transport error.
func ptzDialDetail(err error) string {
	if err == nil {
		return PTZDetailOK
	}
	if isPTZDiscoveryTimeout(err) {
		return PTZDetailAttemptTimeout
	}
	return PTZDetailConnectionError
}

// ptzActionDetail classifies a failure from the action write/ack phase.
func ptzActionDetail(err error) string {
	if err == nil {
		return PTZDetailOK
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timeout waiting for response"):
		return PTZDetailAckTimeout
	case strings.Contains(text, "i/o timeout") || strings.Contains(text, "use of closed"):
		return PTZDetailAckTimeout
	case strings.Contains(text, "invalid ptz direction"):
		return PTZDetailNotAttempted
	default:
		return PTZDetailWriteFailed
	}
}

// ptzAuthDetail classifies an auth_exchange failure. Free-form error text is
// mapped to a fixed label so the WebUI can explain it and so nothing sensitive
// reaches the record.
func ptzAuthDetail(err error) string {
	if err == nil {
		return PTZDetailOK
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "challenge is too short"):
		return PTZDetailAuthShort
	case strings.Contains(text, "authentication challenge"):
		return PTZDetailAuthChallenge
	case strings.Contains(text, "authenticate tutk"):
		return PTZDetailAuthRejected
	default:
		return PTZDetailAuthRejected
	}
}

// ---- bounded history for the WebUI (per camera, sanitized) -------------------

const ptzTraceHistoryLimit = 40

var ptzTraceHistory = struct {
	mu       sync.Mutex
	byCamera map[string][]PTZTraceRecord
	last     map[string]string
}{byCamera: make(map[string][]PTZTraceRecord), last: make(map[string]string)}

func recordPTZTrace(camera string, rec PTZTraceRecord) {
	if camera == "" {
		camera = "unknown"
	}
	ptzTraceHistory.mu.Lock()
	defer ptzTraceHistory.mu.Unlock()
	if ptzTraceHistory.byCamera == nil {
		ptzTraceHistory.byCamera = make(map[string][]PTZTraceRecord)
	}
	if ptzTraceHistory.last == nil {
		ptzTraceHistory.last = make(map[string]string)
	}
	if ptzTraceHistory.last[camera] != rec.Attempt {
		ptzTraceHistory.byCamera[camera] = nil
		ptzTraceHistory.last[camera] = rec.Attempt
	}
	records := append(ptzTraceHistory.byCamera[camera], rec)
	if len(records) > ptzTraceHistoryLimit {
		records = records[len(records)-ptzTraceHistoryLimit:]
	}
	ptzTraceHistory.byCamera[camera] = records
}

// PTZTraceHistory returns the sanitized records from the most recent PTZ
// direction attempt for a camera, oldest first. Records are kept whether or not a
// sink is configured, so the WebUI can always show what happened to the last
// movement or STOP. The history holds only the last attempt: each new attempt
// starts a new attempt id and replaces the previous records.
func PTZTraceHistory(camera string, limit int) []PTZTraceRecord {
	ptzTraceHistory.mu.Lock()
	defer ptzTraceHistory.mu.Unlock()
	records := ptzTraceHistory.byCamera[camera]
	if limit <= 0 || limit > len(records) {
		limit = len(records)
	}
	out := make([]PTZTraceRecord, limit)
	copy(out, records[len(records)-limit:])
	return out
}

// ptzTraceCameraIDs lists cameras that have trace records (for diagnostics UIs).
func ptzTraceCameraIDs() []string {
	ptzTraceHistory.mu.Lock()
	defer ptzTraceHistory.mu.Unlock()
	ids := make([]string, 0, len(ptzTraceHistory.byCamera))
	for camera := range ptzTraceHistory.byCamera {
		ids = append(ids, camera)
	}
	sort.Strings(ids)
	return ids
}
