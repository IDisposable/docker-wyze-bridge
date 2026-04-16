package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestSSEHub_SendAndReceive(t *testing.T) {
	hub := NewSSEHub(zerolog.Nop())

	// Start a test SSE client
	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		// Need a custom ResponseWriter that supports flushing
		hub.ServeHTTP(w, req)
		close(done)
	}()

	// Wait a bit for the client to register
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", hub.ClientCount())
	}

	// Send an event
	hub.Send("test_event", `{"hello":"world"}`)

	// Close the hub to end the SSE handler
	hub.Close()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: test_event") {
		t.Errorf("response should contain event, got: %s", body)
	}
	if !strings.Contains(body, `data: {"hello":"world"}`) {
		t.Errorf("response should contain data, got: %s", body)
	}
}

func TestSSEHub_Headers(t *testing.T) {
	hub := NewSSEHub(zerolog.Nop())

	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()

	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.Close()
	}()

	hub.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

func TestSSEHub_SendJSON(t *testing.T) {
	hub := NewSSEHub(zerolog.Nop())

	req := httptest.NewRequest("GET", "/events", nil)
	w := httptest.NewRecorder()

	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.SendJSON("camera_state", map[string]string{"name": "test", "state": "streaming"})
		time.Sleep(50 * time.Millisecond)
		hub.Close()
	}()

	hub.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "camera_state") {
		t.Errorf("should contain camera_state event: %s", body)
	}
}

// Verify SSEHub implements http.Handler-like ServeHTTP
func TestSSEHub_IsHandler(t *testing.T) {
	hub := NewSSEHub(zerolog.Nop())
	var _ http.HandlerFunc = hub.ServeHTTP
}
