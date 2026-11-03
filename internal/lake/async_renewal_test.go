package lake

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

func newAsyncRenewalSession(t *testing.T, renew func(context.Context, int) (string, error)) (*Session, *websocket.Conn, <-chan message) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	received := make(chan message, 32)
	peerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer peer.Close()
		defer close(peerDone)
		accepted <- peer
		for {
			var m message
			if peer.ReadJSON(&m) != nil {
				return
			}
			select {
			case received <- m:
			default:
			}
		}
	}))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	peer := <-accepted
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{conn: conn, cancel: cancel, stopped: make(chan struct{}), events: make(chan Event, 32),
		initial: &wyzeapi.LakeSession{UID: 12345, UserID: "test-user", Lifetime: 3600},
		started: time.Now(), renewing: renew}
	t.Cleanup(func() {
		s.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("renewal fixture retained a websocket reader")
		}
		server.Close()
	})
	go s.run(ctx)
	asyncRenewalEvent(t, s, "connected")
	return s, peer, received
}

func asyncRenewalEvent(t *testing.T, s *Session, kind string) Event {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-s.events:
			if event.Kind == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("missing renewal event %s", kind)
			return Event{}
		}
	}
}

func triggerAsyncRenewal(t *testing.T, peer *websocket.Conn) {
	t.Helper()
	if err := peer.WriteJSON(message{Type: "on_token_privilege_will_expire", Body: object{}}); err != nil {
		t.Fatal(err)
	}
}

func TestSlowRenewalKeepsPingAndControlFlow(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, peer, received := newAsyncRenewalSession(t, func(ctx context.Context, uid int) (string, error) {
		if uid != 12345 {
			t.Error("renewal changed the viewer identity")
		}
		if calls.Add(1) == 1 {
			close(entered)
			defer close(returned)
		}
		select {
		case <-release:
			return "private-async-renew-token", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	triggerAsyncRenewal(t, peer)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal API did not start")
	}
	for i := 0; i < 8; i++ {
		triggerAsyncRenewal(t, peer)
	}
	// Use the existing production 3s/12s intervals, without test-only timers
	// or deadline knobs. The fake API remains blocked below its 20s deadline.
	timer := time.NewTimer(16 * time.Second)
	defer timer.Stop()
	pings, controls := 0, 0
	for pings < 3 || controls < 1 {
		select {
		case request := <-received:
			switch request.Type {
			case "ping":
				pings++
			case "data_stream":
				payload, err := base64.StdEncoding.DecodeString(str(request.Body["payload"]))
				if err != nil || !strings.Contains(string(payload), "sight-safe::check-user") {
					t.Fatal("periodic ownership control changed")
				}
				controls++
			case "renew_token":
				t.Fatal("token was sent before the blocked API returned")
			}
		case <-returned:
			t.Fatal("API did not stay blocked for the heartbeat observation")
		case <-timer.C:
			t.Fatalf("blocked renewal suppressed periodic traffic: pings=%d controls=%d", pings, controls)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expiry warning burst started overlapping API renewals: %d", calls.Load())
	}
	close(release)
	request := diagnosticRequest(t, received, "renew_token")
	if request.ID == "" || str(request.Body["token"]) != "private-async-renew-token" {
		t.Fatal("async renewal changed the existing token request")
	}
	asyncRenewalEvent(t, s, "token_renewal_sent")
	if err := peer.WriteJSON(message{ID: request.ID, Result: "success", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	asyncRenewalEvent(t, s, "renewal_accepted")
	select {
	case <-s.Done():
		t.Fatal("successful async renewal closed its existing session")
	default:
	}
}

func TestPendingRenewalCancellationIsPrompt(t *testing.T) {
	entered, returned := make(chan struct{}), make(chan struct{})
	s, peer, _ := newAsyncRenewalSession(t, func(ctx context.Context, _ int) (string, error) {
		close(entered)
		<-ctx.Done()
		close(returned)
		return "", ctx.Err()
	})
	triggerAsyncRenewal(t, peer)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal API did not start")
	}
	s.Close()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("session cancellation did not reach the in-flight API")
	}
	asyncRenewalEvent(t, s, "closed")
}

func TestAsyncRenewalFailureStillClosesSession(t *testing.T) {
	for _, token := range []string{"", "ignored-token"} {
		t.Run(map[bool]string{true: "empty-token", false: "api-error"}[token == ""], func(t *testing.T) {
			s, peer, _ := newAsyncRenewalSession(t, func(context.Context, int) (string, error) {
				if token == "" {
					return "", nil
				}
				return token, errors.New("private-async-api-error")
			})
			triggerAsyncRenewal(t, peer)
			asyncRenewalEvent(t, s, "token_renewal_failed")
			asyncRenewalEvent(t, s, "closed")
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("failed renewal no longer tears down its session")
			}
		})
	}
}
