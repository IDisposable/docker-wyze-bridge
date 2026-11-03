package lake

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

// Public encryption key advertised by the Agora WebRTC gateway. This is
// protocol metadata, not a vendored SDK or an account secret.
const gatewayKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDCMnXAHkKIGAM+x4N22gCI+WyuSTM9ztkT3uYslTT2PuKmZfPzhH6kVdO7PTjGCOZnAsyb3oTtWat0KcxQ4jxvqQV+HvYl3iI1Yd4vl2c3qRMJPLtRDfNxa2Mcxgq7e9aEUibzdd0st+OJAy3tOj/Y0aVyxQiYDz3vqa6bP29adwIDAQAB"

func requestID() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b)
}

type edge struct {
	address, fingerprint string
	response             object
}
type message struct {
	ID        string `json:"_id,omitempty"`
	Type      string `json:"_type,omitempty"`
	Body      object `json:"_message"`
	Result    string `json:"_result,omitempty"`
	ErrorCode int    `json:"error_code,omitempty"`
}

// Gateway notifications may use numeric IDs or a primitive/null message body.
// Only object bodies carry fields used by this receive path.
func (m *message) UnmarshalJSON(raw []byte) error {
	var data object
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	m.ID = fmt.Sprint(data["_id"])
	m.Type = str(data["_type"])
	m.Result = str(data["_result"])
	m.Body = obj(data["_message"])
	m.ErrorCode = num(data["error_code"])
	return nil
}

func (m message) diagnosticCode() int {
	if m.ErrorCode != 0 {
		return m.ErrorCode
	}
	if code := num(m.Body["error_code"]); code != 0 {
		return code
	}
	return num(m.Body["code"])
}

func gatewayRequest(ctx context.Context, url string, session *wyzeapi.LakeSession) (*http.Request, error) {
	payload := object{"appid": session.AppID, "client_ts": time.Now().UnixMilli(), "opid": time.Now().UnixNano() % 1000000000000, "sid": requestID(), "request_bodies": []any{object{"uri": 22, "buffer": object{"cname": session.Channel, "detail": object{"11": "CN,GLOBAL", "17": "1", "22": "CN,GLOBAL"}, "key": session.RTCToken, "service_ids": []int{11, 26}, "uid": session.UID}}}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gateway request encoding failed")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="request"`)
	header.Set("Content-Type", "application/json")
	part, err := form.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("gateway form encoding failed")
	}
	_, _ = part.Write(raw)
	if form.Close() != nil {
		return nil, fmt.Errorf("gateway form encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("gateway request failed")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return req, nil

}

func chooseGateway(ctx context.Context, session *wyzeapi.LakeSession) ([]edge, error) {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, host := range []string{"webrtc2-ap-web-1.agora.io", "webrtc2-ap-web-2.agora.io", "webrtc2-ap-web-3.agora.io"} {
		req, err := gatewayRequest(ctx, "https://"+host+"/api/v2/transpond/webrtc?v=2", session)
		if err != nil {
			return nil, fmt.Errorf("gateway request failed")
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || len(raw) > 1024*1024 {
			continue
		}
		var data object
		if json.Unmarshal(raw, &data) != nil {
			continue
		}
		var result []edge
		for _, v := range array(data["response_body"]) {
			b := obj(obj(v)["buffer"])
			if num(b["code"]) != 0 || num(b["flag"]) != 4096 {
				continue
			}
			detail := object{}
			for k, v := range obj(data["detail"]) {
				detail[k] = v
			}
			for k, v := range obj(b["detail"]) {
				detail[k] = v
			}
			fps := strings.Split(str(detail["19"]), ";")
			ap := object{"code": 0, "server_ts": data["enter_ts"], "uid": b["uid"], "cid": b["cid"], "cname": b["cname"], "detail": detail, "flag": 4096, "opid": data["opid"], "cert": b["cert"], "ticket": b["cert"]}
			for i, v := range array(b["edges_services"]) {
				e := obj(v)
				ip := net.ParseIP(str(e["ip"]))
				port := num(e["port"])
				if ip == nil || ip.To4() == nil || port < 1 || port > 65535 {
					continue
				}
				fp := ""
				if i < len(fps) {
					fp = strings.TrimSpace(fps[i])
					if fp != "" && !strings.Contains(fp, " ") {
						fp = "sha-256 " + fp
					}
				}
				result = append(result, edge{address: fmt.Sprintf("wss://%s.edge.agora.io:%d", strings.ReplaceAll(ip.String(), ".", "-"), port), fingerprint: fp, response: ap})
			}
		}
		if len(result) > 0 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("no Agora WebRTC gateway available")
}

func encryptedSecret(secret string) (string, error) {
	der, e := base64.StdEncoding.DecodeString(gatewayKey)
	if e != nil {
		return "", fmt.Errorf("invalid gateway encryption key")
	}
	key, e := x509.ParsePKIXPublicKey(der)
	if e != nil {
		return "", fmt.Errorf("invalid gateway encryption key")
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("invalid gateway key type")
	}
	cipher, e := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, []byte(secret), nil)
	if e != nil {
		return "", fmt.Errorf("gateway key wrapping failed")
	}
	return base64.StdEncoding.EncodeToString(cipher), nil
}

// Session owns the gateway websocket. It outlives go2rtc's short offer/answer
// socket, which go2rtc intentionally closes once ICE is connected.
type Event struct {
	Kind          string
	Code          int
	Width, Height int // Only populated for decoded quality samples.
}

const (
	activationSendFailed   = "activation_send_failed"
	subscriptionSendFailed = "subscription_send_failed"
	pingSendFailed         = "ping_send_failed"
)

type Session struct {
	events    chan Event
	conn      *websocket.Conn
	mu        sync.Mutex
	stopped   chan struct{}
	once      sync.Once
	cancel    context.CancelFunc
	initial   *wyzeapi.LakeSession
	renewing  func(context.Context, int) (string, error)
	quality   string
	started   time.Time
	renewID   string // Protected by mu; only the latest renewal reply is observed.
	videoUID  int64  // Negotiated video identity, used only to classify notices.
	videoSSRC int64
}

func (s *Session) Close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		_ = s.conn.Close()
		close(s.stopped)
	})
}
func (s *Session) Events() <-chan Event { return s.events }
func (s *Session) notify(kind string, code int) {
	select {
	case s.events <- Event{Kind: kind, Code: code}:
	default:
	}
}
func (s *Session) Done() <-chan struct{} { return s.stopped }
func (s *Session) send(kind string, body object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return e
	}
	id := requestID()
	err := s.conn.WriteJSON(message{ID: id, Type: kind, Body: body})
	if kind == "renew_token" {
		s.renewID = ""
		if err == nil {
			s.renewID = id
		}
	}
	return err
}

func (s *Session) notifyReadError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	var closeErr *websocket.CloseError
	var networkErr net.Error
	switch {
	case errors.As(err, &closeErr):
		// Code 1006 can be synthesized for EOF; it does not prove a remote
		// close frame. Never expose the gateway's close text or raw error.
		s.notify("gateway_read_closed", closeErr.Code)
	case errors.As(err, &networkErr) && networkErr.Timeout():
		s.notify("gateway_read_timeout", 0)
	default:
		s.notify("gateway_read_failed", 0)
	}
}

func (s *Session) notifyRenewalReply(m message) bool {
	if m.Result != "success" && m.Result != "error" {
		return false
	}
	s.mu.Lock()
	matched := s.renewID != "" && s.renewID == m.ID
	if matched {
		s.renewID = ""
	}
	s.mu.Unlock()
	if matched {
		kind := "renewal_accepted"
		if m.Result == "error" {
			kind = "renewal_rejected"
		}
		s.notify(kind, m.diagnosticCode())
	}
	return matched
}

func (s *Session) notifyVideoStream(stream object) {
	kind := "video_stream_announced"
	uid, ssrc := number64(stream["uid"]), number64(stream["ssrcId"])
	if s.videoUID > 0 && s.videoSSRC > 0 && uid > 0 && ssrc > 0 {
		kind = "video_stream_same"
		if uid != s.videoUID || ssrc != s.videoSSRC {
			kind = "video_stream_changed"
		}
	}
	s.notify(kind, 0)
}

func (s *Session) notifyUserOffline(m message) {
	kind := "user_offline_unknown"
	uid := number64(m.Body["uid"])
	// Compare only valid RTC UIDs with the originally negotiated publisher.
	// A fractional JSON number must not be truncated into a false match.
	if uid > 0 && uid <= 4294967295 && s.videoUID > 0 && s.videoUID <= 4294967295 {
		n, numeric := m.Body["uid"].(float64)
		if !numeric || n == float64(uid) {
			kind = "user_offline_other"
			if uid == s.videoUID {
				kind = "user_offline_publisher"
			}
		}
	}
	// Reason has no verified numeric mapping for this wire notice. Preserve
	// existing diagnostic codes without exposing identities or raw payloads.
	s.notify(kind, m.diagnosticCode())
}
func (s *Session) subscribe(stream object) error {
	return s.send("subscribe", object{"stream_id": stream["uid"], "stream_type": "video", "mode": "rtc", "codec": "h265", "p2p_id": 1, "twcc": false, "rtx": true, "extend": "", "ssrcId": stream["ssrcId"]})
}
func (s *Session) controls(quality bool) error {
	commands := []any{object{"cmd": "run_action", "action": "sight-safe::check-user", "params": object{"userId": s.initial.UserID}}}
	if quality {
		commands = append(commands, object{"cmd": "set_property", "props": object{"camera::resolution": s.quality}})
	}
	raw, e := json.Marshal(commands)
	if e != nil {
		return e
	}
	return s.send("data_stream", object{"payload": base64.StdEncoding.EncodeToString(raw), "syncWithAudio": false, "sendTs": time.Since(s.started).Milliseconds()})
}

// Continue ownership checks while limiting periodic quality retries to startup.
func (s *Session) periodicControls(elapsed time.Duration) error {
	return s.controls(elapsed > 20*time.Second && elapsed < 100*time.Second)
}

func Open(ctx context.Context, initial *wyzeapi.LakeSession, sdp, quality string, renew func(context.Context, int) (string, error)) (*Session, string, error) {
	if initial == nil || initial.UID < 10000 || initial.UID > 99999 || initial.AppID == "" || initial.Channel == "" || initial.RTCToken == "" || initial.UserID == "" {
		return nil, "", fmt.Errorf("incomplete Lake session")
	}
	o, e := parseOffer(sdp)
	if e != nil {
		return nil, "", e
	}
	if initial.EncryptionMode != 7 {
		return nil, "", fmt.Errorf("unsupported Lake encryption")
	}
	if quality != "2k" && quality != "360p" {
		return nil, "", fmt.Errorf("unsupported Lake quality")
	}
	secret, e := encryptedSecret(initial.EncryptionKey)
	if e != nil {
		return nil, "", e
	}
	edges, e := chooseGateway(ctx, initial)
	if e != nil {
		return nil, "", e
	}
	return openEdges(ctx, initial, o, quality, renew, secret, edges)
}

func openEdges(ctx context.Context, initial *wyzeapi.LakeSession, o *offer, quality string, renew func(context.Context, int) (string, error), secret string, edges []edge) (*Session, string, error) {
	lastReason := "gateway connection failed"
	for _, edge := range edges {
		dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
		conn, _, err := dialer.DialContext(ctx, edge.address, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			continue
		}
		conn.SetReadLimit(1024 * 1024)
		runCtx, cancel := context.WithCancel(ctx)
		s := &Session{conn: conn, stopped: make(chan struct{}), events: make(chan Event, 32), cancel: cancel, initial: initial, renewing: renew, quality: quality, started: time.Now()}
		context.AfterFunc(runCtx, s.Close)
		msg := object{"uid": initial.UID, "p2p_id": 1, "session_id": requestID(), "app_id": initial.AppID, "channel_key": initial.RTCToken, "channel_name": initial.Channel, "sdk_version": "4.24.0", "browser": "Wyze Bridge WebRTC receiver", "process_id": "process-" + requestID(), "mode": "rtc", "codec": "h265", "role": "host", "has_changed_gateway": false, "ap_response": edge.response, "extend": "", "details": object{}, "features": object{"rejoin": true}, "attributes": object{"userAttributes": object{"enablePublishedUserList": true, "maxSubscription": 50, "enableUserLicenseCheck": true, "enableRTX": true, "enableInstantVideo": false, "enableDataStream2": false, "enableAutFeedback": true, "enableUserAutoRebalanceCheck": true, "enableXR": true, "enableLossbasedBwe": true, "enableAutCC": true, "enablePreallocPC": false, "enablePubTWCC": false, "enableSubTWCC": true, "enablePubRTX": true, "enableSubRTX": true}}, "join_ts": time.Now().UnixMilli(), "ortc": o.ortc(), "default_video_stream": 0, "aes_mode": "aes-128-gcm2", "aes_secret": secret, "aes_salt": initial.EncryptionSalt, "aes_encrypt": true}
		if s.send("join_v3", msg) != nil {
			s.Close()
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		var ortc, stream object
		for {
			var msg message
			if conn.ReadJSON(&msg) != nil {
				lastReason = "gateway response timed out or closed"
				break
			}
			if msg.Result == "error" {
				lastReason = fmt.Sprintf("gateway rejected join (code %d)", msg.diagnosticCode())
				break
			}
			if msg.Result == "success" && len(obj(msg.Body["ortc"])) > 0 {
				ortc = obj(msg.Body["ortc"])
				// A camera that has stopped publishing may wait for its viewer
				// ownership command before advertising a video stream. Send it
				// after joining, rather than waiting for video first.
				if s.controls(false) != nil {
					lastReason = "initial camera control failed"
					break
				}
			}
			if msg.Type == "on_user_online" && num(msg.Body["uid"]) > 0 && num(msg.Body["uid"]) != initial.UID {
				// The first control may precede the camera's arrival. Request
				// ownership and quality again once its publisher is online.
				if s.controls(true) != nil {
					lastReason = "camera activation failed"
					break
				}
			}
			if msg.Type == "on_add_video_stream" && msg.Body["video"] == true && num(msg.Body["ssrcId"]) != 0 {
				stream = msg.Body
				if s.subscribe(stream) != nil {
					break
				}
			}
			if ortc != nil && stream != nil {
				answer, e := o.answer(ortc, stream, edge.fingerprint)
				if e != nil {
					lastReason = e.Error()
					break
				}
				_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
				s.videoUID, s.videoSSRC = number64(stream["uid"]), number64(stream["ssrcId"])
				go s.run(runCtx)
				return s, answer, nil
			}
		}
		s.Close()
	}
	return nil, "", fmt.Errorf("Agora WebRTC negotiation failed: %s", lastReason)
}
func (s *Session) run(ctx context.Context) {
	defer s.Close()
	defer s.notify("closed", 0)
	s.notify("connected", 0)
	readErr := make(chan error, 1)
	renewNeeded := make(chan struct{}, 1)
	go func() {
		for {
			var m message
			e := s.conn.ReadJSON(&m)
			if e != nil {
				s.notifyReadError(ctx, e)
				readErr <- e
				return
			}
			_ = s.conn.SetReadDeadline(time.Now().Add(45 * time.Second))
			if !s.notifyRenewalReply(m) && m.Result == "error" {
				s.notify("gateway_request_rejected", m.diagnosticCode())
			}
			if m.Type == "on_token_privilege_will_expire" {
				s.notify("token_expiry_warning", m.diagnosticCode())
				select {
				case renewNeeded <- struct{}{}:
				default:
				}
			}
			if m.Type == "on_p2p_lost" || m.Type == "on_token_privilege_did_expire" {
				kind := "gateway_peer_lost"
				if m.Type == "on_token_privilege_did_expire" {
					kind = "gateway_token_expired"
				}
				s.notify(kind, m.diagnosticCode())
				readErr <- fmt.Errorf("gateway disconnected")
				return
			}
			if m.Type == "on_remove_video_stream" {
				s.notify("video_stream_removed", m.diagnosticCode())
			}
			if m.Type == "on_user_offline" {
				s.notifyUserOffline(m)
			}
			if m.Type == "on_user_online" && num(m.Body["uid"]) > 0 && num(m.Body["uid"]) != s.initial.UID {
				if s.controls(true) != nil {
					s.notify(activationSendFailed, 0)
					readErr <- fmt.Errorf("camera activation failed")
					return
				}
			}
			if m.Type == "on_add_video_stream" && m.Body["video"] == true {
				s.notifyVideoStream(m.Body)
				if s.subscribe(m.Body) != nil {
					s.notify(subscriptionSendFailed, 0)
					readErr <- fmt.Errorf("subscription failed")
					return
				}
			}
		}
	}()
	ping := time.NewTicker(3 * time.Second)
	defer ping.Stop()
	control := time.NewTicker(12 * time.Second)
	defer control.Stop()
	begin := time.Now()
	renewAfter := time.Duration(s.initial.Lifetime) * time.Second * 2 / 3
	if renewAfter < 30*time.Second {
		renewAfter = 30 * time.Second
	}
	renew := time.NewTicker(renewAfter)
	defer renew.Stop()
	renewalDone := make(chan bool, 1)
	renewalPending := false
	startRenewal := func() {
		if renewalPending || ctx.Err() != nil {
			return
		}
		renewalPending = true
		go func() { renewalDone <- s.renew(ctx) }()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			return
		case <-ping.C:
			if s.send("ping", object{}) != nil {
				s.notify(pingSendFailed, 0)
				return
			}
		case <-control.C:
			if s.periodicControls(time.Since(begin)) != nil {
				s.notify("control_send_failed", 0)
				return
			}
		case <-renew.C:
			startRenewal()
		case <-renewNeeded:
			startRenewal()
		case renewed := <-renewalDone:
			renewalPending = false
			if !renewed {
				return
			}

		}
	}
}

func (s *Session) renew(ctx context.Context) bool {
	if s.renewing == nil {
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	token, e := s.renewing(rctx, s.initial.UID)
	cancel()
	if e != nil || token == "" {
		s.notify("token_renewal_failed", 0)
		return false
	}
	if s.send("renew_token", object{"token": token}) != nil {
		s.notify("token_renewal_send_failed", 0)
		return false
	}
	s.notify("token_renewal_sent", 0)
	return true
}
