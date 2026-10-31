package lake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

const testSDP = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nt=0 0\r\na=group:BUNDLE 0 1 2\r\nm=video 9 UDP/TLS/RTP/SAVPF 96 100\r\na=ice-ufrag:local\r\na=ice-pwd:local-password\r\na=fingerprint:sha-256 AA:BB\r\na=mid:0\r\na=recvonly\r\na=rtpmap:96 H264/90000\r\na=rtpmap:100 H265/90000\r\na=rtcp-fb:100 nack pli\r\na=extmap:1 urn:ietf:params:rtp-hdrext:sdes:mid\r\nm=audio 9 UDP/TLS/RTP/SAVPF 0\r\na=mid:1\r\na=recvonly\r\na=rtpmap:0 PCMU/8000\r\nm=audio 9 UDP/TLS/RTP/SAVPF 0\r\na=mid:2\r\na=sendonly\r\na=rtpmap:0 PCMU/8000\r\n"

func TestHEVCNegotiation(t *testing.T) {
	o, e := parseOffer(testSDP)
	if e != nil {
		t.Fatal(e)
	}
	caps := obj(obj(o.ortc()["rtpCapabilities"])["recv"])
	video := array(caps["videoCodecs"])
	if len(video) != 1 || str(obj(obj(video[0])["rtpMap"])["encodingName"]) != "H265" {
		t.Fatal("offer did not select HEVC")
	}
	ortc := object{"iceParameters": object{"iceUfrag": "remote", "icePwd": "remote-password", "candidates": []any{object{"ip": "127.0.0.1", "port": 12345}}}, "dtlsParameters": object{}, "rtpCapabilities": object{"sendrecv": caps}}
	answer, e := o.answer(ortc, object{"ssrcId": 1234, "cname": "camera"}, "sha-256 AA:BB")
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"a=group:BUNDLE 0 1 2", "a=rtpmap:100 H265/90000", "a=candidate:gateway0 1 udp 2103266323 127.0.0.1 12345 typ host", "a=ssrc:1234 msid:lake lake-video"} {
		if !strings.Contains(answer, s) {
			t.Fatalf("answer missing %s", s)
		}
	}
	if strings.Contains(answer, " H264/") {
		t.Fatal("answer reintroduced H264")
	}
	if strings.Count(answer, "a=inactive") != 2 {
		t.Fatal("audio was not disabled")
	}
}
func TestInvalidOffers(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("a", 65537), strings.ReplaceAll(testSDP, "H265", "VP8"), strings.ReplaceAll(testSDP, "a=ice-pwd:local-password\r\n", "")} {
		if _, e := parseOffer(s); e == nil {
			t.Fatal("accepted invalid offer")
		}
	}
}
func TestGatewayEncryption(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	a, e := encryptedSecret(secret)
	if e != nil {
		t.Fatal(e)
	}
	b, e := encryptedSecret(secret)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := base64.StdEncoding.DecodeString(a)
	if e != nil || len(raw) != 128 {
		t.Fatal("wrong encrypted key size")
	}
	if a == b || strings.Contains(a, secret) {
		t.Fatal("key wrapping did not randomize/hide secret")
	}
}
func TestGatewayJoinAndCancellation(t *testing.T) {
	for _, streamFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream-first", false: "join-first"}[streamFirst], func(t *testing.T) {
			o, e := parseOffer(testSDP)
			if e != nil {
				t.Fatal(e)
			}
			caps := obj(obj(o.ortc()["rtpCapabilities"])["recv"])
			done := make(chan struct{})
			joins := make(chan message, 1)
			subscriptions := make(chan message, 1)
			renewed := make(chan message, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u := websocket.Upgrader{}
				c, e := u.Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer c.Close()
				defer close(done)
				var join message
				if c.ReadJSON(&join) != nil {
					return
				}
				joins <- join
				success := message{Result: "success", Body: object{"ortc": object{"iceParameters": object{"iceUfrag": "remote", "icePwd": "remote-password"}, "dtlsParameters": object{"fingerprints": []any{object{"algorithm": "sha-256", "fingerprint": "AA:BB"}}}, "rtpCapabilities": object{"sendrecv": caps}}}}
				stream := message{Type: "on_add_video_stream", Body: object{"uid": 777, "video": true, "ssrcId": 1234, "cname": "camera"}}
				if streamFirst {
					_ = c.WriteJSON(stream)
					_ = c.WriteJSON(success)
				} else {
					_ = c.WriteJSON(success)
					// Cold cameras need ownership before advertising video.
					var control message
					if c.ReadJSON(&control) != nil || control.Type != "data_stream" {
						return
					}
					payload, _ := base64.StdEncoding.DecodeString(str(control.Body["payload"]))
					if !strings.Contains(string(payload), "sight-safe::check-user") {
						return
					}
					_ = c.WriteJSON(message{Type: "on_user_online", Body: object{"uid": 777}})
					if c.ReadJSON(&control) != nil || control.Type != "data_stream" {
						return
					}
					payload, _ = base64.StdEncoding.DecodeString(str(control.Body["payload"]))
					if !strings.Contains(string(payload), `"camera::resolution":"2k"`) {
						return
					}
					_ = c.WriteJSON(stream)
				}
				for {
					var sub message
					if c.ReadJSON(&sub) != nil {
						return
					}
					if sub.Type == "subscribe" {
						subscriptions <- sub
						break
					}
				}
				_ = c.WriteJSON(message{Type: "on_token_privilege_will_expire", Body: object{}})
				for {
					var m message
					if c.ReadJSON(&m) != nil {
						return
					}
					if m.Type == "renew_token" {
						renewed <- m
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			initial := &wyzeapi.LakeSession{AppID: "app", UID: 12345, Channel: "channel", RTCToken: "secret-token", EncryptionMode: 7, EncryptionKey: "secret-key", EncryptionSalt: base64.StdEncoding.EncodeToString(make([]byte, 32)), UserID: "private-user", Lifetime: 3600}
			s, answer, e := openEdges(ctx, initial, o, "2k", func(context.Context, int) (string, error) { return "renewed-test-token", nil }, "wrapped-key", []edge{{address: "ws" + strings.TrimPrefix(server.URL, "http"), response: object{"ticket": "ticket"}}})
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(answer, "H265") {
				t.Fatal("no HEVC answer")
			}
			if s.videoUID != 777 || s.videoSSRC != 1234 {
				t.Fatal("diagnostics did not retain the negotiated video identity")
			}
			join := <-joins
			if str(join.Body["mode"]) != "rtc" || str(join.Body["role"]) != "host" || str(join.Body["aes_secret"]) != "wrapped-key" {
				t.Fatal("wrong communication/encryption join")
			}
			sub := <-subscriptions
			if sub.Type != "subscribe" || str(sub.Body["codec"]) != "h265" {
				t.Fatal("wrong video subscription")
			}
			select {
			case msg := <-renewed:
				if str(msg.Body["token"]) != "renewed-test-token" {
					t.Fatal("wrong renewed token")
				}
			case <-time.After(time.Second):
				t.Fatal("expiry notification did not renew token")
			}
			cancel()
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("session did not stop")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("gateway reader leaked")
			}
		})
	}
}
func TestBrokerCloseWithoutSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := NewBroker(ctx, nil)
	b.CloseCamera("unknown")
	b.Close()
	cancel()
	if b.Active() != 0 {
		t.Fatal("unexpected session")
	}
}

func TestSessionLevelTransport(t *testing.T) {
	s := strings.ReplaceAll(testSDP, "a=fingerprint:sha-256 AA:BB\r\n", "")
	s = strings.Replace(s, "a=group:BUNDLE 0 1 2\r\n", "a=group:BUNDLE 0 1 2\r\na=fingerprint:sha-256 AA:BB\r\n", 1)
	if _, e := parseOffer(s); e != nil {
		t.Fatal(e)
	}
}

func TestGatewayMultipartRequest(t *testing.T) {
	r, e := gatewayRequest(context.Background(), "https://gateway.example/api", &wyzeapi.LakeSession{AppID: "app", UID: 12345, Channel: "channel", RTCToken: "private-token"})
	if e != nil {
		t.Fatal(e)
	}
	if e := r.ParseMultipartForm(1 << 20); e != nil {
		t.Fatal(e)
	}
	var data object
	if json.Unmarshal([]byte(r.FormValue("request")), &data) != nil {
		t.Fatal("missing gateway JSON form field")
	}
	if str(data["appid"]) != "app" || num(obj(obj(array(data["request_bodies"])[0])["buffer"])["uid"]) != 12345 {
		t.Fatal("wrong gateway request shape")
	}
}

func TestGatewayFingerprintAlgorithmField(t *testing.T) {
	o, e := parseOffer(testSDP)
	if e != nil {
		t.Fatal(e)
	}
	caps := obj(obj(o.ortc()["rtpCapabilities"])["recv"])
	answer, e := o.answer(object{"iceParameters": object{"iceUfrag": "remote", "icePwd": "password"}, "dtlsParameters": object{"fingerprints": []any{object{"algorithm": "sha-256", "fingerprint": "AA:BB"}}}, "rtpCapabilities": object{"recv": caps}}, object{"ssrcId": 1234}, "")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(answer, "a=fingerprint:sha-256 AA:BB") {
		t.Fatal("gateway algorithm field lost")
	}
}

func TestPrimitiveGatewayNotifications(t *testing.T) {
	for _, raw := range []string{`{"_id":0,"_type":"ping_back","_message":0}`, `{"_type":"notification","_message":null}`, `{"_type":"notification","_message":[]}`} {
		var m message
		if json.Unmarshal([]byte(raw), &m) != nil {
			t.Fatal("valid gateway notification closed the session")
		}
	}
}

func TestUnsignedSSRC(t *testing.T) {
	o, _ := parseOffer(testSDP)
	caps := obj(obj(o.ortc()["rtpCapabilities"])["recv"])
	answer, e := o.answer(object{"iceParameters": object{"iceUfrag": "remote", "icePwd": "password"}, "rtpCapabilities": object{"recv": caps}}, object{"ssrcId": float64(4294967295)}, "sha-256 AA:BB")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(answer, "a=ssrc:4294967295") {
		t.Fatal("SSRC overflow")
	}
}
