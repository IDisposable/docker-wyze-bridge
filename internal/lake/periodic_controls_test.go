package lake

import (
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

func TestPeriodicControlsBoundStartupQuality(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer peer.Close()
		accepted <- peer
		<-done
	}))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		close(done)
		server.Close()
		t.Fatal(err)
	}
	peer := <-accepted
	t.Cleanup(func() {
		_ = conn.Close()
		_ = peer.Close()
		close(done)
		server.Close()
	})
	s := &Session{conn: conn, initial: &wyzeapi.LakeSession{UserID: "test-user"}, quality: "2k", started: time.Now()}
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		quality bool
	}{
		{"initial", 0, false},
		{"lower-boundary", 20 * time.Second, false},
		{"after-lower-boundary", 20*time.Second + time.Nanosecond, true},
		{"first-periodic-retry", 24 * time.Second, true},
		{"last-periodic-retry", 96 * time.Second, true},
		{"before-upper-boundary", 100*time.Second - time.Nanosecond, true},
		{"upper-boundary", 100 * time.Second, false},
		{"after-startup", 120 * time.Second, false},
		{"established-session", time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.periodicControls(tc.elapsed); err != nil {
				t.Fatal(err)
			}
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var request message
			if err := peer.ReadJSON(&request); err != nil {
				t.Fatal(err)
			}
			if request.Type != "data_stream" || request.Body["syncWithAudio"] != false {
				t.Fatal("periodic control transport changed")
			}
			payload, err := base64.StdEncoding.DecodeString(str(request.Body["payload"]))
			if err != nil {
				t.Fatal(err)
			}
			var commands []object
			if err := json.Unmarshal(payload, &commands); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.quality {
				want = 2
			}
			if len(commands) != want {
				t.Fatalf("elapsed %s: got %d commands, want %d", tc.elapsed, len(commands), want)
			}
			if str(commands[0]["cmd"]) != "run_action" || str(commands[0]["action"]) != "sight-safe::check-user" || str(obj(commands[0]["params"])["userId"]) != "test-user" {
				t.Fatal("periodic ownership check changed")
			}
			if tc.quality && (str(commands[1]["cmd"]) != "set_property" || str(obj(commands[1]["props"])["camera::resolution"]) != "2k") {
				t.Fatal("startup quality command changed")
			}
		})
	}
}
