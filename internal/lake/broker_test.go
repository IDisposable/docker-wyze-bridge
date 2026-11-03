package lake

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

type blockedProvider struct {
	started  chan struct{}
	canceled chan struct{}
}

func (p *blockedProvider) GetLakeSession(ctx context.Context, _, _ string) (*wyzeapi.LakeSession, error) {
	p.started <- struct{}{}
	<-ctx.Done()
	p.canceled <- struct{}{}
	return nil, ctx.Err()
}
func (*blockedProvider) RenewLakeToken(context.Context, string, string, int) (string, error) {
	panic("unexpected renewal")
}

func TestBrokerCancelsPendingCamera(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "camera-stop", true: "shutdown"}[shutdown], func(t *testing.T) {
			provider := &blockedProvider{make(chan struct{}, 1), make(chan struct{}, 1)}
			b := NewBroker(context.Background(), provider)
			defer b.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k", nil)
			}))
			defer server.Close()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err = c.WriteJSON(object{"type": "webrtc/offer", "value": testSDP}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-provider.started:
			case <-time.After(time.Second):
				t.Fatal("provisioning did not start")
			}
			b.CloseIdleCamera("front")
			select {
			case <-provider.canceled:
				t.Fatal("idle check canceled a pending negotiation")
			default:
			}
			if shutdown {
				b.Close()
			} else {
				b.CloseCamera("front")
			}
			select {
			case <-provider.canceled:
			case <-time.After(time.Second):
				t.Fatal("pending provisioning was not canceled")
			}
			c.SetReadDeadline(time.Now().Add(time.Second))
			var reply object
			if c.ReadJSON(&reply) != nil || str(reply["type"]) != "error" {
				t.Fatal("failed provisioning did not finish safely")
			}
			b.mu.Lock()
			pending := len(b.pending)
			b.mu.Unlock()
			if pending != 0 || b.Active() != 0 {
				t.Fatal("pending operation or session leaked")
			}
		})
	}
}

func TestBrokerStopCancelsQueuedCamera(t *testing.T) {
	provider := &blockedProvider{make(chan struct{}, 2), make(chan struct{}, 2)}
	b := NewBroker(context.Background(), provider)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k", nil)
	}))
	defer func() { b.Close(); server.Close() }()
	clients := make([]*websocket.Conn, 2)
	for i := range clients {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = c
		defer c.Close()
		if err = c.WriteJSON(object{"type": "webrtc/offer", "value": testSDP}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			select {
			case <-provider.started:
			case <-time.After(time.Second):
				t.Fatal("first negotiation did not start")
			}
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		accepted := b.epochs["front"] == 2
		b.mu.Unlock()
		if accepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second offer did not reach the camera queue")
		}
		time.Sleep(time.Millisecond)
	}
	b.CloseCamera("front")
	for _, c := range clients {
		c.SetReadDeadline(time.Now().Add(time.Second))
		var reply object
		if c.ReadJSON(&reply) != nil || str(reply["type"]) != "error" {
			t.Fatal("stop did not finish both pending offers")
		}
	}
	select {
	case <-provider.started:
		t.Fatal("queued offer provisioned a new session after stop")
	default:
	}
	if b.Active() != 0 {
		t.Fatal("stopped camera retained a session")
	}
}

func TestBrokerStopRejectsSocketWaitingForOffer(t *testing.T) {
	for _, mode := range []string{"camera-stop", "shutdown", "inactive-at-admission", "inactive-waiting-offer"} {
		t.Run(mode, func(t *testing.T) {
			provider := &blockedProvider{make(chan struct{}, 1), make(chan struct{}, 1)}
			b := NewBroker(context.Background(), provider)
			var active atomic.Bool
			active.Store(mode != "inactive-at-admission")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k", active.Load)
			}))
			defer func() { b.Close(); server.Close() }()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			// The accepted socket is still waiting for SDP when the user stops
			// the camera. Its later offer must not recreate the stopped session.
			switch mode {
			case "shutdown":
				b.Close()
			case "camera-stop":
				b.CloseCamera("front")
			default:
				// The shim may have checked activity before Broker admission.
				// Reject an offline camera even if its stop generation predates
				// this socket, or changes while the socket waits for its offer.
				active.Store(false)
			}
			if err = c.WriteJSON(object{"type": "webrtc/offer", "value": testSDP}); err != nil {
				t.Fatal(err)
			}
			c.SetReadDeadline(time.Now().Add(time.Second))
			var reply object
			if c.ReadJSON(&reply) != nil || str(reply["type"]) != "error" {
				t.Error("late offer did not finish safely after stop")
			}
			select {
			case <-provider.started:
				t.Error("late offer provisioned a new session after stop")
			default:
			}
			if b.Active() != 0 {
				t.Fatal("stopped camera retained a session")
			}
		})
	}
}

func TestGatewayFailureClosesSessionAndReader(t *testing.T) {
	for _, tc := range []struct{ name, notification, event string }{
		{"renewal-failure", "on_token_privilege_will_expire", "token_renewal_failed"},
		{"expired-token", "on_token_privilege_did_expire", "gateway_token_expired"},
		{"lost-peer", "on_p2p_lost", "gateway_peer_lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseOffer(testSDP)
			if err != nil {
				t.Fatal(err)
			}
			caps := obj(obj(o.ortc()["rtpCapabilities"])["recv"])
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close()
				defer close(done)
				var join message
				if c.ReadJSON(&join) != nil {
					return
				}
				_ = c.WriteJSON(message{Result: "success", Body: object{"ortc": object{
					"iceParameters":   object{"iceUfrag": "remote", "icePwd": "remote-password"},
					"dtlsParameters":  object{"fingerprints": []any{object{"algorithm": "sha-256", "fingerprint": "AA:BB"}}},
					"rtpCapabilities": object{"recv": caps},
				}}})
				_ = c.WriteJSON(message{Type: "on_add_video_stream", Body: object{"uid": 777, "video": true, "ssrcId": 1234}})
				for {
					var m message
					if c.ReadJSON(&m) != nil {
						return
					}
					if m.Type == "subscribe" {
						_ = c.WriteJSON(message{Type: tc.notification, Body: object{}})
						break
					}
				}
				for {
					var m message
					if c.ReadJSON(&m) != nil {
						return
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			initial := &wyzeapi.LakeSession{AppID: "app", UID: 12345, Channel: "channel", RTCToken: "test-token", UserID: "test-user", Lifetime: 3600}
			s, _, err := openEdges(ctx, initial, o, "2k", func(context.Context, int) (string, error) {
				return "", context.DeadlineExceeded
			}, "wrapped-key", []edge{{address: "ws" + strings.TrimPrefix(server.URL, "http")}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("failed gateway session did not stop")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("failed gateway session left its reader open")
			}
			found := false
			for len(s.events) > 0 {
				found = (<-s.events).Kind == tc.event || found
			}
			if !found {
				t.Fatal("failed gateway session did not report its reason")
			}
		})
	}
}
