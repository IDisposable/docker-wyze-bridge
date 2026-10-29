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

func TestQualityPolicySustainedDropCooldownAndBudget(t *testing.T) {
	var p qualityPolicy
	now := time.Now()
	low, high := Resolution{640, 360}, Resolution{3840, 2160}
	check := func(minutes int, r Resolution, err error, wantEvent string, wantRetry bool) {
		t.Helper()
		event, retry := p.observe(now.Add(time.Duration(minutes)*time.Minute), r, err)
		if event != wantEvent || retry != wantRetry {
			t.Fatalf("minute %d got %s/%t want %s/%t", minutes, event, retry, wantEvent, wantRetry)
		}
	}
	check(0, high, nil, "", false)
	check(1, low, nil, "", false)
	check(2, high, nil, "", false) // a brief change does not trigger
	check(3, low, nil, "", false)
	check(4, Resolution{}, errors.New("sample failed"), "", false)
	check(5, low, nil, "", false)
	check(6, Resolution{}, nil, "", false) // idle also breaks confirmation
	check(7, low, nil, "", false)
	check(8, low, nil, "quality_low", true)
	check(9, low, nil, "", false)
	check(10, low, nil, "", true)
	check(11, high, nil, "quality_recovered", false)
	check(12, low, nil, "", false)
	check(13, low, nil, "quality_low", true) // recovery did not refill budget
	check(14, low, nil, "", false)
	check(15, low, nil, "quality_retry_exhausted", false)
	check(67, low, nil, "", false)
	check(68, low, nil, "", true)
	check(69, low, nil, "", false)
	check(70, low, nil, "", true) // only expired attempts replenish the budget
}

func TestQualityRetryUsesExistingGatewayWithoutReplacingSession(t *testing.T) {
	writes := make(chan message, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer peer.Close()
		for {
			var m message
			if peer.ReadJSON(&m) != nil {
				return
			}
			writes <- m
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{conn: conn, stopped: make(chan struct{}), events: make(chan Event, 32), initial: &wyzeapi.LakeSession{UserID: "test-user"}, quality: "2k", started: time.Now()}
	defer s.Close()
	b, op := qualityTestBroker(s)
	defer b.cancel()
	var calls atomic.Int32
	b.ProbeQuality = func(context.Context, string) (Resolution, error) { calls.Add(1); return Resolution{640, 360}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.runQualityGuard(ctx, "front", s, op, time.Millisecond, 10*time.Millisecond); close(done) }()
	select {
	case m := <-writes:
		if calls.Load() < 2 || m.Type != "data_stream" {
			t.Fatal("retry sent before confirmed low samples")
		}
		payload, err := base64.StdEncoding.DecodeString(str(m.Body["payload"]))
		if err != nil || !strings.Contains(string(payload), `"camera::resolution":"2k"`) {
			t.Fatal("missing high-quality property")
		}
	case <-time.After(time.Second):
		t.Fatal("no quality retry sent")
	}
	cancel()
	<-done
	if b.sessions["front"] != s || b.Active() != 1 {
		t.Fatal("retry replaced the camera session")
	}
}

func qualityTestBroker(s *Session) (*Broker, *cameraOperation) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broker{ctx: ctx, cancel: cancel, sessions: map[string]*Session{}, pending: map[string]*cameraOperation{}, epochs: map[string]uint64{}}
	op := &cameraOperation{epoch: 1}
	b.sessions["front"], b.pending["front"], b.epochs["front"] = s, op, 1
	return b, op
}

func TestQualityGuardStopsActiveProbeOnSessionClose(t *testing.T) {
	s := &Session{stopped: make(chan struct{}), events: make(chan Event, 32)}
	b, op := qualityTestBroker(s)
	defer b.cancel()
	started, done := make(chan struct{}), make(chan struct{})
	b.ProbeQuality = func(ctx context.Context, _ string) (Resolution, error) {
		close(started)
		<-ctx.Done()
		return Resolution{}, ctx.Err()
	}
	go func() {
		b.runQualityGuard(context.Background(), "front", s, op, time.Millisecond, time.Millisecond)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe not started")
	}
	close(s.stopped)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe was not cancelled")
	}
	if len(s.events) != 0 {
		t.Fatal("closed session emitted quality event")
	}
}

func TestQualityGuardRejectsReplacedSessionAndWarmup(t *testing.T) {
	s := &Session{stopped: make(chan struct{}), events: make(chan Event, 32)}
	b, op := qualityTestBroker(s)
	defer b.cancel()
	var calls atomic.Int32
	b.ProbeQuality = func(context.Context, string) (Resolution, error) {
		calls.Add(1)
		b.mu.Lock()
		b.epochs["front"]++
		b.mu.Unlock()
		return Resolution{640, 360}, nil
	}
	done := make(chan struct{})
	go func() {
		b.runQualityGuard(context.Background(), "front", s, op, 50*time.Millisecond, time.Millisecond)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("probe ran before warmup")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old guard did not exit")
	}
	if calls.Load() != 1 || len(s.events) != 0 {
		t.Fatal("stale probe result accepted")
	}
	if b.requestQuality("front", s, op) {
		t.Fatal("stale session received command")
	}
}
