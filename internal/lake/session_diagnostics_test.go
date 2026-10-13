package lake

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

func TestBrokerDrainsBufferedEventsBeforeRelease(t *testing.T) {
	for _, callback := range []bool{true, false} {
		t.Run(map[bool]string{true: "callback", false: "no-callback"}[callback], func(t *testing.T) {
			s := &Session{events: make(chan Event, 4), stopped: make(chan struct{})}
			close(s.stopped)
			op := &cameraOperation{cancel: func() {}}
			b := &Broker{sessions: map[string]*Session{"front": s}, pending: map[string]*cameraOperation{"front": op}}
			want := []Event{{Kind: "gateway_read_failed"}, {Kind: "closed"}}
			for _, event := range want {
				s.events <- event
			}
			var got []Event
			if callback {
				b.OnEvent = func(name string, event Event) {
					if name != "front" || b.Active() != 1 {
						t.Error("buffered diagnostic forwarded after ownership release")
					}
					got = append(got, event)
				}
			}
			b.drainSessionEvents("front", s)
			b.release("front", s, op)
			if len(s.events) != 0 || (callback && !reflect.DeepEqual(got, want)) {
				t.Fatalf("buffered diagnostics lost: %v, remaining %d", got, len(s.events))
			}
			if b.Active() != 0 || len(b.pending) != 0 {
				t.Fatal("existing ownership cleanup changed")
			}
		})
	}
}

func TestBrokerDiagnosticDrainIsCapacityBounded(t *testing.T) {
	s := &Session{events: make(chan Event, 4)}
	for i := 0; i < cap(s.events); i++ {
		s.events <- Event{Kind: "buffered"}
	}
	var calls atomic.Int32
	var refill atomic.Bool
	refill.Store(true)
	defer refill.Store(false)
	b := &Broker{OnEvent: func(_ string, event Event) {
		calls.Add(1)
		// Simulate continuing publication without letting the queue empty.
		if refill.Load() {
			s.events <- Event{Kind: "later"}
		}
	}}
	done := make(chan struct{})
	go func() { b.drainSessionEvents("front", s); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostic drain waited for an actively refilled queue")
	}
	if calls.Load() != int32(cap(s.events)) {
		t.Fatalf("expected exactly original capacity callbacks, got %d", calls.Load())
	}
}

func TestBrokerDiagnosticDrainDoesNotWaitForLateEvents(t *testing.T) {
	s := &Session{events: make(chan Event, 4), stopped: make(chan struct{})}
	close(s.stopped)
	var calls atomic.Int32
	b := &Broker{OnEvent: func(string, Event) { calls.Add(1) }}
	done := make(chan struct{})
	go func() { b.drainSessionEvents("front", s); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("empty diagnostic drain introduced a wait")
	}
	// External Close may precede a later run notification. That remains
	// best effort; this diagnostic-only change must not delay teardown.
	s.events <- Event{Kind: "closed"}
	if calls.Load() != 0 {
		t.Fatal("late external-close event unexpectedly acquired a new forwarder")
	}
}

type failingDiagnosticWriteConn struct {
	net.Conn
	fail atomic.Bool
}

func (c *failingDiagnosticWriteConn) Write(data []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("private-token-in-test-error")
	}
	return c.Conn.Write(data)
}

func TestSessionNotifiesExistingSendFailures(t *testing.T) {
	for _, tc := range []struct{ name, notification, event string }{
		{"activation", "on_user_online", "activation_send_failed"},
		{"subscription", "on_add_video_stream", "subscription_send_failed"},
		{"ping", "", "ping_send_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted := make(chan *websocket.Conn, 1)
			peerDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				defer close(peerDone)
				accepted <- conn
				for {
					var message object
					if conn.ReadJSON(&message) != nil {
						return
					}
				}
			}))
			defer server.Close()
			var network *failingDiagnosticWriteConn
			dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, protocol, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, protocol, address)
				if err != nil {
					return nil, err
				}
				network = &failingDiagnosticWriteConn{Conn: conn}
				return network, nil
			}}
			conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			peer := <-accepted
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &Session{conn: conn, cancel: cancel, stopped: make(chan struct{}), events: make(chan Event, 8),
				initial: &wyzeapi.LakeSession{UID: 12345, Lifetime: 3600}}
			defer s.Close()
			go s.run(ctx)
			select {
			case event := <-s.events:
				if event.Kind != "connected" {
					t.Fatal("session failed to start")
				}
			case <-time.After(time.Second):
				t.Fatal("session failed to start")
			}
			network.fail.Store(true)
			if tc.notification != "" {
				if err := peer.WriteJSON(message{Type: tc.notification, Body: object{"uid": 54321, "video": true}}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-s.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("existing send failure no longer closed the session")
			}
			var got []Event
			for len(s.events) > 0 {
				got = append(got, <-s.events)
			}
			want := []Event{{Kind: tc.event}, {Kind: "closed"}}
			if tc.name == "subscription" {
				want = append([]Event{{Kind: "video_stream_announced"}}, want...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("missing safe notification before physical close: got %v want %v", got, want)
			}
			select {
			case <-peerDone:
			case <-time.After(time.Second):
				t.Fatal("existing failure cleanup left its websocket open")
			}
		})
	}
}

type diagnosticTimeoutError struct{}

func (diagnosticTimeoutError) Error() string   { return "private-read-token" }
func (diagnosticTimeoutError) Timeout() bool   { return true }
func (diagnosticTimeoutError) Temporary() bool { return false }

func TestSessionReadDiagnosticsAreTypedAndSanitized(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Event
	}{
		{"timeout", diagnosticTimeoutError{}, Event{Kind: "gateway_read_timeout"}},
		{"close", &websocket.CloseError{Code: 1001, Text: "private-close-token"}, Event{Kind: "gateway_read_closed", Code: 1001}},
		{"abnormal-eof", &websocket.CloseError{Code: 1006, Text: "private-eof-token"}, Event{Kind: "gateway_read_closed", Code: 1006}},
		{"other", errors.New("private-other-token"), Event{Kind: "gateway_read_failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{events: make(chan Event, 2)}
			s.notifyReadError(context.Background(), tc.err)
			if got := <-s.events; got != tc.want {
				t.Fatalf("unsafe or incorrect read category: %+v", got)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.notifyReadError(ctx, tc.err)
			if len(s.events) != 0 {
				t.Fatal("local cancellation was reported as a gateway failure")
			}
		})
	}
}

// Real loopback WebSockets exercise the existing gateway reader/writer without
// contacting Wyze or Agora. The peer keeps draining outbound controls unchanged.
func newDiagnosticSession(t *testing.T) (*Session, *websocket.Conn, <-chan message) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	received := make(chan message, 16)
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
		initial: &wyzeapi.LakeSession{UID: 12345, Lifetime: 3600}, started: time.Now(),
		renewing: func(context.Context, int) (string, error) { return "private-renew-token", nil },
		videoUID: 777, videoSSRC: 1234}
	t.Cleanup(func() {
		s.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("diagnostic session cleanup left a gateway reader open")
		}
		server.Close()
	})
	go s.run(ctx)
	if event := diagnosticEvent(t, s); event.Kind != "connected" {
		t.Fatal("gateway fixture did not connect")
	}
	return s, peer, received
}

func diagnosticEvent(t *testing.T, s *Session) Event {
	t.Helper()
	select {
	case event := <-s.events:
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), "private-") {
			t.Fatal("diagnostic exposed a private protocol value")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("gateway diagnostic did not arrive")
		return Event{}
	}
}

func diagnosticRequest(t *testing.T, received <-chan message, kind string) message {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-received:
			if m.Type == kind {
				return m
			}
		case <-timer.C:
			t.Fatalf("existing %s request was not sent", kind)
			return message{}
		}
	}
}

func TestSessionObservesOnlyLatestMatchingRenewalReply(t *testing.T) {
	s, peer, received := newDiagnosticSession(t)
	if !s.renew(context.Background()) {
		t.Fatal("fixture renewal failed")
	}
	first := diagnosticRequest(t, received, "renew_token")
	if event := diagnosticEvent(t, s); event.Kind != "token_renewal_sent" {
		t.Fatalf("send-only renewal was mislabeled: %+v", event)
	}
	if !s.renew(context.Background()) {
		t.Fatal("fixture second renewal failed")
	}
	second := diagnosticRequest(t, received, "renew_token")
	if first.ID == "" || second.ID == "" || first.ID == second.ID {
		t.Fatal("renewals did not retain distinct existing request IDs")
	}
	if event := diagnosticEvent(t, s); event.Kind != "token_renewal_sent" {
		t.Fatal("second send was not observed")
	}
	for _, response := range []any{
		object{"_id": first.ID, "_result": "success", "_message": "private-stale-reply"},
		object{"_id": "unrelated-request", "_result": "error", "error_code": 999, "error_str": "private-unrelated-error"},
		object{"_id": second.ID, "_result": "progress", "_message": "private-nonterminal-reply"},
		object{"_type": "on_user_offline", "_message": nil},
	} {
		if err := peer.WriteJSON(response); err != nil {
			t.Fatal(err)
		}
	}
	if event := diagnosticEvent(t, s); event != (Event{Kind: "gateway_request_rejected", Code: 999}) {
		t.Fatalf("unrelated rejection was not safely observed: %+v", event)
	}
	if event := diagnosticEvent(t, s); event.Kind != "user_offline" {
		t.Fatalf("stale/nonterminal reply was treated as acceptance: %+v", event)
	}
	if err := peer.WriteJSON(object{"_id": second.ID, "_result": "error", "error_code": 109,
		"error_str": "private-rejection-text", "_message": "private-reply-body"}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event != (Event{Kind: "renewal_rejected", Code: 109}) {
		t.Fatalf("matching primitive-body rejection was lost: %+v", event)
	}
	select {
	case <-s.Done():
		t.Fatal("diagnostic reply handling changed session behavior")
	default:
	}
	if err := peer.WriteJSON(message{ID: second.ID, Result: "success", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteJSON(message{Type: "on_remove_video_stream", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event.Kind != "video_stream_removed" {
		t.Fatalf("consumed renewal ID produced a duplicate event: %+v", event)
	}
	if !s.renew(context.Background()) {
		t.Fatal("renewal after rejection changed")
	}
	third := diagnosticRequest(t, received, "renew_token")
	if event := diagnosticEvent(t, s); event.Kind != "token_renewal_sent" {
		t.Fatal("third send was not observed")
	}
	if err := peer.WriteJSON(message{ID: third.ID, Result: "success", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event != (Event{Kind: "renewal_accepted"}) {
		t.Fatalf("matching acceptance was not observed: %+v", event)
	}
}

func TestSessionStreamAndExpiryNoticesDoNotChangeProtocol(t *testing.T) {
	s, peer, received := newDiagnosticSession(t)
	for _, tc := range []struct {
		body object
		kind string
	}{
		{object{"uid": 777, "ssrcId": 1234, "video": true}, "video_stream_same"},
		{object{"uid": 778, "ssrcId": 1234, "video": true}, "video_stream_changed"},
		{object{"uid": 777, "ssrcId": 1235, "video": true}, "video_stream_changed"},
		{object{"uid": 777, "ssrcId": 1234, "video": true}, "video_stream_same"},
		{object{"video": true}, "video_stream_announced"},
	} {
		if err := peer.WriteJSON(message{Type: "on_add_video_stream", Body: tc.body}); err != nil {
			t.Fatal(err)
		}
		if event := diagnosticEvent(t, s); event != (Event{Kind: tc.kind}) {
			t.Fatalf("stream notice lost its safe negotiated comparison: %+v", event)
		}
		sub := diagnosticRequest(t, received, "subscribe")
		if num(sub.Body["stream_id"]) != num(tc.body["uid"]) || num(sub.Body["ssrcId"]) != num(tc.body["ssrcId"]) {
			t.Fatal("diagnostics changed the existing subscription")
		}
	}
	if err := peer.WriteJSON(message{Type: "on_token_privilege_will_expire", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event.Kind != "token_expiry_warning" {
		t.Fatal("expiry warning was not observed")
	}
	if renewal := diagnosticRequest(t, received, "renew_token"); str(renewal.Body["token"]) != "private-renew-token" {
		t.Fatal("expiry warning changed existing token renewal")
	}
	if event := diagnosticEvent(t, s); event.Kind != "token_renewal_sent" {
		t.Fatal("warning-triggered send was not observed")
	}
}

func TestSessionGatewayDisconnectKeepsSafeNumericReason(t *testing.T) {
	for _, tc := range []struct {
		name, notification, kind string
		body                     any
		topCode, code            int
	}{
		{"peer-top-level-primitive", "on_p2p_lost", "gateway_peer_lost", "private-body", 17, 17},
		{"expired-body-error", "on_token_privilege_did_expire", "gateway_token_expired", object{"error_code": 109}, 0, 109},
		{"peer-body-code", "on_p2p_lost", "gateway_peer_lost", object{"code": 23}, 0, 23},
		{"top-level-precedence", "on_p2p_lost", "gateway_peer_lost", object{"error_code": 99, "code": 88}, 17, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, peer, _ := newDiagnosticSession(t)
			if err := peer.WriteJSON(object{"_type": tc.notification, "_message": tc.body,
				"error_code": tc.topCode, "error_str": "private-gateway-error"}); err != nil {
				t.Fatal(err)
			}
			if event := diagnosticEvent(t, s); event != (Event{Kind: tc.kind, Code: tc.code}) {
				t.Fatalf("numeric disconnect reason was lost: %+v", event)
			}
			if event := diagnosticEvent(t, s); event.Kind != "closed" {
				t.Fatalf("disconnect no longer follows existing cleanup: %+v", event)
			}
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("disconnect no longer closes the session")
			}
		})
	}
}

func TestSessionGatewayCloseFrameIsObservedWithoutText(t *testing.T) {
	s, peer, _ := newDiagnosticSession(t)
	if err := peer.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1001, "private-close-reason"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event != (Event{Kind: "gateway_read_closed", Code: 1001}) {
		t.Fatalf("gateway close was not safely classified: %+v", event)
	}
	if event := diagnosticEvent(t, s); event.Kind != "closed" {
		t.Fatal("gateway close did not follow existing cleanup")
	}
}

func TestSessionOtherRequestRejectionDoesNotChangeBehavior(t *testing.T) {
	s, peer, _ := newDiagnosticSession(t)
	if err := peer.WriteJSON(object{"_id": "private-control-request", "_type": "private-response-type",
		"_result": "error", "error_code": 27, "error_str": "private-rejection-reason",
		"_message": object{"error_code": 99, "channel_key": "private-channel-token"}}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event != (Event{Kind: "gateway_request_rejected", Code: 27}) {
		t.Fatalf("other request rejection was not safely retained: %+v", event)
	}
	if err := peer.WriteJSON(message{Type: "on_user_offline", Body: object{}}); err != nil {
		t.Fatal(err)
	}
	if event := diagnosticEvent(t, s); event.Kind != "user_offline" {
		t.Fatalf("other rejection changed or duplicated reader processing: %+v", event)
	}
	select {
	case <-s.Done():
		t.Fatal("diagnostics changed handling of an existing nonterminal rejection")
	default:
	}
}

func TestSessionLocalCancellationDoesNotClaimGatewayFailure(t *testing.T) {
	s, _, _ := newDiagnosticSession(t)
	s.cancel()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("diagnostics delayed existing local cancellation")
	}
	if event := diagnosticEvent(t, s); event.Kind != "closed" {
		t.Fatalf("local cancellation was misreported: %+v", event)
	}
	select {
	case event := <-s.events:
		t.Fatalf("local cancellation added a remote failure: %+v", event)
	default:
	}
}
