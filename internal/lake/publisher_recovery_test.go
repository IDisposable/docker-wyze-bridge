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

func TestBrokerPublisherOfflineRequestsReconnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  any
		want bool
	}{
		{"publisher", 777, true},
		{"other user", 888, false},
		{"unknown user", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, peer, writes := newOfflineDiagnosticSession(t, 777)
			b := NewBroker(context.Background(), nil)
			t.Cleanup(b.Close)
			op := &cameraOperation{cancel: func() {}, epoch: 1}
			b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
			if b.NeedsReconnect("front") || b.NeedsReconnect("never-started") {
				t.Fatal("startup requested reconnection without publisher loss")
			}
			if err := peer.WriteJSON(message{Type: "on_user_offline", Body: object{"uid": tc.uid}}); err != nil {
				t.Fatal(err)
			}
			b.observeSessionEvent("front", s, op, diagnosticEvent(t, s))
			if b.NeedsReconnect("front") != tc.want || b.NeedsReconnect("side") {
				t.Fatal("publisher loss was missed or crossed camera ownership")
			}
			select {
			case <-s.Done():
				t.Fatal("observing loss closed the gateway before demanded health recovery")
			case m := <-writes:
				t.Fatalf("observing loss sent a new protocol command: %s", m.Type)
			default:
			}
			b.release("front", s, op)
			if b.NeedsReconnect("front") != tc.want {
				t.Fatal("session release lost the outstanding publisher departure")
			}
			b.CloseIdleCamera("front")
			if b.NeedsReconnect("front") {
				t.Fatal("idle camera retained a reconnection request")
			}
		})
	}
}

func TestBrokerPublisherLossOwnershipAndIntentionalReset(t *testing.T) {
	for _, action := range []string{"superseding epoch", "replacement session", "camera stop", "idle", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			s, _, _ := newOfflineDiagnosticSession(t, 777)
			s.started = time.Now().Add(-2 * time.Minute)
			b := NewBroker(context.Background(), nil)
			t.Cleanup(b.Close)
			op := &cameraOperation{cancel: func() {}, epoch: 1}
			b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
			switch action {
			case "superseding epoch":
				b.epochs["front"] = 2
			case "replacement session":
				next, _, _ := newOfflineDiagnosticSession(t, 777)
				b.sessions["front"] = next
			case "camera stop", "idle", "shutdown":
				b.observeSessionEvent("front", s, op, Event{Kind: "user_offline_publisher"})
				if !b.NeedsReconnect("front") {
					t.Fatal("current departure was not recorded")
				}
				switch action {
				case "camera stop":
					b.CloseCamera("front")
				case "idle":
					b.CloseIdleCamera("front")
				case "shutdown":
					b.Close()
				}
			}
			// A buffered completion from the old reader must not invalidate a
			// replacement or undo an intentional stop/idle/shutdown.
			s.events <- Event{Kind: "user_offline_publisher"}
			b.drainSessionEvents("front", s, op)
			if b.NeedsReconnect("front") {
				t.Fatal("stale publisher event requested reconnection")
			}
		})
	}
}

func TestBrokerRecordedPublisherLossDoesNotAbortNewerOffer(t *testing.T) {
	s, _, _ := newOfflineDiagnosticSession(t, 777)
	b := NewBroker(context.Background(), nil)
	defer b.Close()
	op := &cameraOperation{cancel: func() {}, epoch: 1}
	b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
	b.observeSessionEvent("front", s, op, Event{Kind: "user_offline_publisher"})
	if !b.NeedsReconnect("front") {
		t.Fatal("original publisher loss was not recorded")
	}
	// ServeCamera accepts a newer epoch before acquiring the per-camera
	// negotiation lock. The old recorded loss must not cancel that offer.
	b.epochs["front"]++
	if b.NeedsReconnect("front") {
		t.Fatal("old publisher loss invalidated a newly accepted offer")
	}
}

func TestBrokerRecordedPublisherLossDoesNotAbortPendingReplacement(t *testing.T) {
	s, _, _ := newOfflineDiagnosticSession(t, 777)
	b := NewBroker(context.Background(), nil)
	defer b.Close()
	op := &cameraOperation{cancel: func() {}, epoch: 1}
	b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
	b.observeSessionEvent("front", s, op, Event{Kind: "user_offline_publisher"})
	// A new pending owner must suppress the old loss even if the old session
	// remains present during the handover.
	next := &cameraOperation{cancel: func() {}, epoch: 2}
	b.pending["front"], b.epochs["front"] = next, 2
	if b.NeedsReconnect("front") {
		t.Fatal("old publisher loss invalidated a pending replacement")
	}
}

func TestBrokerReleasedPublisherLossDoesNotAbortNewerOffer(t *testing.T) {
	s, _, _ := newOfflineDiagnosticSession(t, 777)
	b := NewBroker(context.Background(), nil)
	defer b.Close()
	op := &cameraOperation{cancel: func() {}, epoch: 1}
	b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
	b.observeSessionEvent("front", s, op, Event{Kind: "user_offline_publisher"})
	b.release("front", s, op)
	if !b.NeedsReconnect("front") {
		t.Fatal("normal release forgot original publisher departure")
	}
	b.epochs["front"]++
	if b.NeedsReconnect("front") {
		t.Fatal("released publisher loss invalidated a newly accepted offer")
	}
}

type rejectedPublisherReplacementProvider struct {
	started chan struct{}
	proceed chan struct{}
}

func (p *rejectedPublisherReplacementProvider) GetLakeSession(ctx context.Context, _, _ string) (*wyzeapi.LakeSession, error) {
	p.started <- struct{}{}
	select {
	case <-p.proceed:
	case <-ctx.Done():
	}
	return nil, context.DeadlineExceeded
}

func (*rejectedPublisherReplacementProvider) RenewLakeToken(context.Context, string, string, int) (string, error) {
	panic("unexpected renewal")
}

func TestBrokerPublisherLossFailedReplacementOwnership(t *testing.T) {
	for _, action := range []string{"failed replacement", "stop", "shutdown", "newer offer"} {
		t.Run(action, func(t *testing.T) {
			provider := &rejectedPublisherReplacementProvider{make(chan struct{}, 1), make(chan struct{}, 1)}
			b := NewBroker(context.Background(), provider)
			old := &cameraOperation{cancel: func() {}, epoch: 1}
			b.publisherLost["front"], b.epochs["front"] = old, 1
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				b.ServeCamera(w, r, "front", "test", "HL_PAN4", "2k", nil)
			}))
			defer func() {
				b.Close()
				close(provider.proceed)
				server.Close()
			}()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.WriteJSON(object{"type": "webrtc/offer", "value": testSDP}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-provider.started:
			case <-time.After(time.Second):
				t.Fatal("replacement provisioning did not start")
			}
			if b.NeedsReconnect("front") {
				t.Fatal("replacement negotiation did not receive startup grace")
			}
			switch action {
			case "stop":
				b.CloseCamera("front")
			case "shutdown":
				b.Close()
			case "newer offer":
				b.mu.Lock()
				b.epochs["front"]++
				b.mu.Unlock()
			}
			provider.proceed <- struct{}{}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("failed replacement retained its handler")
			}
			if b.NeedsReconnect("front") != (action == "failed replacement") {
				t.Fatal("failed cleanup forgot original loss or re-armed stopped/superseded ownership")
			}
		})
	}
}
