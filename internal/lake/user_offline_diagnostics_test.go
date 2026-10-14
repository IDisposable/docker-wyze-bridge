package lake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

// Exercise the existing reader over a real websocket; an offline notice must
// remain diagnostic only, with no new send, close or subscription behavior.
func newOfflineDiagnosticSession(t *testing.T, videoUID int64) (*Session, *websocket.Conn, <-chan message) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	writes := make(chan message, 16)
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
			var m message
			if conn.ReadJSON(&m) != nil {
				return
			}
			writes <- m
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{conn: conn, cancel: cancel, stopped: make(chan struct{}), events: make(chan Event, 16),
		initial: &wyzeapi.LakeSession{UID: 12345, Lifetime: 3600}, videoUID: videoUID, videoSSRC: 1234}
	go s.run(ctx)
	t.Cleanup(func() {
		cancel()
		s.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("reader cleanup left its websocket open")
		}
	})
	select {
	case e := <-s.Events():
		if e.Kind != "connected" {
			t.Fatal("session did not start")
		}
	case <-time.After(time.Second):
		t.Fatal("session startup timed out")
	}
	return s, peer, writes
}

func TestUserOfflineDiagnosticsClassifyWithoutLifecycleChange(t *testing.T) {
	s, peer, writes := newOfflineDiagnosticSession(t, 777)
	for _, tc := range []struct {
		name, raw, kind string
		code            int
	}{
		{"publisher number", `{"_type":"on_user_offline","_message":{"uid":777}}`, "user_offline_publisher", 0},
		{"publisher string", `{"_type":"on_user_offline","_message":{"uid":"777"}}`, "user_offline_publisher", 0},
		{"other number", `{"_type":"on_user_offline","_message":{"uid":888}}`, "user_offline_other", 0},
		{"other string", `{"_type":"on_user_offline","_message":{"uid":"888"}}`, "user_offline_other", 0},
		{"self viewer", `{"_type":"on_user_offline","_message":{"uid":12345}}`, "user_offline_other", 0},
		{"missing uid", `{"_type":"on_user_offline","_message":{}}`, "user_offline_unknown", 0},
		{"null uid", `{"_type":"on_user_offline","_message":{"uid":null}}`, "user_offline_unknown", 0},
		{"null body", `{"_type":"on_user_offline","_message":null}`, "user_offline_unknown", 0},
		{"primitive body", `{"_type":"on_user_offline","_message":0}`, "user_offline_unknown", 0},
		{"invalid string", `{"_type":"on_user_offline","_message":{"uid":"private-uid"}}`, "user_offline_unknown", 0},
		{"fractional number", `{"_type":"on_user_offline","_message":{"uid":777.25}}`, "user_offline_unknown", 0},
		{"zero", `{"_type":"on_user_offline","_message":{"uid":0}}`, "user_offline_unknown", 0},
		{"negative", `{"_type":"on_user_offline","_message":{"uid":-1}}`, "user_offline_unknown", 0},
		{"outside rtc uid range", `{"_type":"on_user_offline","_message":{"uid":4294967296}}`, "user_offline_unknown", 0},
		{"reason is not a code", `{"_type":"on_user_offline","_message":{"uid":777,"reason":17}}`, "user_offline_publisher", 0},
		{"top code preserved", `{"_type":"on_user_offline","error_code":14,"_message":{"uid":777,"error_code":15,"code":16,"reason":17}}`, "user_offline_publisher", 14},
		{"body error preserved", `{"_type":"on_user_offline","_message":{"uid":888,"error_code":15,"code":16}}`, "user_offline_other", 15},
		{"body string code preserved", `{"_type":"on_user_offline","_message":{"uid":777,"code":"16"}}`, "user_offline_publisher", 16},
		{"private payload", `{"_type":"on_user_offline","_id":"private-request","_message":{"uid":"777","reason":"private-reason","token":"private-token","channel":"private-channel"}}`, "user_offline_publisher", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := peer.WriteMessage(websocket.TextMessage, []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-s.Events():
				if e != (Event{Kind: tc.kind, Code: tc.code}) {
					t.Fatalf("classification/code changed: got %v want %s/%d", e, tc.kind, tc.code)
				}
				serialized, err := json.Marshal(e)
				if err != nil || strings.Contains(string(serialized), "private-") || strings.Contains(string(serialized), "777") || strings.Contains(string(serialized), "12345") {
					t.Fatal("diagnostic exposed an identifier or private payload")
				}
			case <-time.After(time.Second):
				t.Fatal("offline notice did not produce a diagnostic")
			}
			select {
			case <-s.Done():
				t.Fatal("diagnostic-only offline notice closed the session")
			default:
			}
		})
	}
	select {
	case m := <-writes:
		t.Fatalf("offline diagnostics sent a new protocol message: %s", m.Type)
	default:
	}
	// Prove the reader remains live and its existing subscribe behavior still
	// executes after both related and unrelated offline diagnostics.
	if err := peer.WriteJSON(message{Type: "on_add_video_stream", Body: object{"uid": 777, "ssrcId": 1234, "video": true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-writes:
		if m.Type != "subscribe" {
			t.Fatalf("existing subscribe behavior changed: %s", m.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("reader stopped processing after offline notice")
	}
}

func TestUserOfflineWithoutKnownVideoUIDRemainsUnknown(t *testing.T) {
	s, peer, _ := newOfflineDiagnosticSession(t, 0)
	if err := peer.WriteJSON(message{Type: "on_user_offline", Body: object{"uid": 777}}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.Events():
		if e != (Event{Kind: "user_offline_unknown"}) {
			t.Fatal("unknown negotiated publisher was guessed")
		}
	case <-time.After(time.Second):
		t.Fatal("unknown publisher notice was lost")
	}
}
