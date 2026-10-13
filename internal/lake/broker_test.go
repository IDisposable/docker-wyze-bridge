package lake

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k") }))
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
		b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k")
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
