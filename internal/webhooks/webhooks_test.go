package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestParseURLs(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"http://example.com/hook", 1},
		{"http://a.com,http://b.com", 2},
		{" http://a.com , http://b.com , ", 2},
		{",,,,", 0},
	}
	for _, tt := range tests {
		got := ParseURLs(tt.input)
		if len(got) != tt.want {
			t.Errorf("ParseURLs(%q) = %d URLs, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestClient_Enabled(t *testing.T) {
	c := NewClient(Config{}, zerolog.Nop())
	if c.Enabled() {
		t.Error("no URLs should not be enabled")
	}

	c = NewClient(Config{URLs: []string{"http://example.com"}}, zerolog.Nop())
	if !c.Enabled() {
		t.Error("with URLs should be enabled")
	}
}

func TestClient_Send(t *testing.T) {
	var received atomic.Int32
	var lastPayload Payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)

		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		if ev := r.Header.Get("X-Wyze-Bridge-Event"); ev == "" {
			t.Error("missing X-Wyze-Bridge-Event header")
		}

		json.NewDecoder(r.Body).Decode(&lastPayload)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(Config{
		URLs:    []string{srv.URL},
		Timeout: 5 * time.Second,
	}, zerolog.Nop())

	ctx := context.Background()
	c.Send(ctx, EventCameraOnline, "front_door", map[string]interface{}{
		"ip":    "192.168.1.10",
		"model": "HL_CAM4",
	})

	// Wait for async delivery
	time.Sleep(100 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("received = %d, want 1", received.Load())
	}
	if lastPayload.Event != EventCameraOnline {
		t.Errorf("event = %q", lastPayload.Event)
	}
	if lastPayload.Camera != "front_door" {
		t.Errorf("camera = %q", lastPayload.Camera)
	}
	if lastPayload.Data["ip"] != "192.168.1.10" {
		t.Errorf("data.ip = %v", lastPayload.Data["ip"])
	}
}

func TestClient_Send_MultipleURLs(t *testing.T) {
	var count1, count2 atomic.Int32

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.WriteHeader(200)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(200)
	}))
	defer srv2.Close()

	c := NewClient(Config{
		URLs: []string{srv1.URL, srv2.URL},
	}, zerolog.Nop())

	c.Send(context.Background(), EventCameraOffline, "cam", nil)

	time.Sleep(100 * time.Millisecond)

	if count1.Load() != 1 || count2.Load() != 1 {
		t.Errorf("both URLs should receive: %d, %d", count1.Load(), count2.Load())
	}
}

func TestClient_Send_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	c := NewClient(Config{URLs: []string{srv.URL}}, zerolog.Nop())
	// Should not panic on error response
	c.Send(context.Background(), EventCameraError, "cam", nil)
	time.Sleep(100 * time.Millisecond)
}

func TestClient_Send_Disabled(t *testing.T) {
	c := NewClient(Config{}, zerolog.Nop())
	// Should be a no-op, not panic
	c.Send(context.Background(), EventCameraOnline, "cam", nil)
}

func TestClient_Send_UnreachableURL(t *testing.T) {
	c := NewClient(Config{
		URLs:    []string{"http://192.0.2.1:9999/unreachable"},
		Timeout: 100 * time.Millisecond,
	}, zerolog.Nop())
	// Should not panic or hang
	c.Send(context.Background(), EventCameraOnline, "cam", nil)
	time.Sleep(200 * time.Millisecond)
}

func TestClient_SendHelpers(t *testing.T) {
	var events []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ev := r.Header.Get("X-Wyze-Bridge-Event")
		events = append(events, ev)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(Config{URLs: []string{srv.URL}}, zerolog.Nop())
	ctx := context.Background()

	c.SendCameraOnline(ctx, "cam1", nil)
	c.SendCameraOffline(ctx, "cam2", nil)
	c.SendCameraError(ctx, "cam3", nil)
	c.SendSnapshotReady(ctx, "cam4")

	time.Sleep(200 * time.Millisecond)

	if len(events) != 4 {
		t.Errorf("expected 4 events, got %d", len(events))
	}
}

func TestFormatCameraData(t *testing.T) {
	data := FormatCameraData("10.0.0.1", "HL_CAM4", "4.52.9", "AABB", "hd")
	if data["ip"] != "10.0.0.1" {
		t.Errorf("ip = %v", data["ip"])
	}
	if data["model"] != "HL_CAM4" {
		t.Errorf("model = %v", data["model"])
	}
	if data["quality"] != "hd" {
		t.Errorf("quality = %v", data["quality"])
	}
}

func TestPayload_JSON(t *testing.T) {
	p := Payload{
		Event:     EventCameraOnline,
		Camera:    "test",
		Timestamp: time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		Data:      map[string]interface{}{"key": "val"},
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var parsed Payload
	json.Unmarshal(data, &parsed)

	if parsed.Event != "camera_online" {
		t.Errorf("event = %q", parsed.Event)
	}
	if parsed.Camera != "test" {
		t.Errorf("camera = %q", parsed.Camera)
	}
}
