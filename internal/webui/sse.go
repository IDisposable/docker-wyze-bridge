package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// SSEHub manages Server-Sent Events connections.
//
// Besides the bounded-channel per-client backpressure, the hub also
// dedups consecutive identical payloads within a short window. Callers
// like the camera state-change broadcaster can fire repeatedly without
// worrying about spamming idle clients — if the last payload for a
// given event name matches byte-for-byte, the re-send is skipped.
type SSEHub struct {
	log     zerolog.Logger
	clients map[chan string]struct{}
	mu      sync.RWMutex
	closed  bool
	// lastSent tracks the most recent payload per event name for
	// dedup. Keyed by event type (e.g. "camera_state"), value is the
	// full SSE-formatted message.
	lastSent map[string]sseSent
}

type sseSent struct {
	msg  string
	when time.Time
}

// sseDedupWindow is how long an identical payload is considered a
// duplicate. 500ms catches "two callbacks fired back-to-back for the
// same state transition" without blocking legitimate repeat events.
const sseDedupWindow = 500 * time.Millisecond

// NewSSEHub creates a new SSE hub.
func NewSSEHub(log zerolog.Logger) *SSEHub {
	return &SSEHub{
		log:      log,
		clients:  make(map[chan string]struct{}),
		lastSent: make(map[string]sseSent),
	}
}

// ServeHTTP handles SSE connections.
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan string, 64)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		// Don't close ch here; Close() may have already closed it.
		// The channel will be GC'd.
	}()

	// Send initial heartbeat
	fmt.Fprintf(w, ": heartbeat\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		}
	}
}

// Send broadcasts an SSE event to all connected clients. Consecutive
// identical payloads within sseDedupWindow are silently dropped.
func (h *SSEHub) Send(event, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)

	h.mu.Lock()
	// Dedup: same event+payload within the window = skip.
	if prev, ok := h.lastSent[event]; ok && prev.msg == msg && time.Since(prev.when) < sseDedupWindow {
		h.mu.Unlock()
		return
	}
	h.lastSent[event] = sseSent{msg: msg, when: time.Now()}
	clients := make([]chan string, 0, len(h.clients))
	for ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- msg:
		default:
			// Client too slow, drop message
			h.log.Debug().Str("event", event).Msg("SSE client too slow, dropping message")
		}
	}
}

// SendJSON broadcasts a JSON SSE event.
func (h *SSEHub) SendJSON(event string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		h.log.Error().Err(err).Str("event", event).Msg("SSE JSON marshal failed")
		return
	}
	h.Send(event, string(b))
}

// Close shuts down the SSE hub.
func (h *SSEHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
}

// ClientCount returns the number of connected SSE clients.
func (h *SSEHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SendHeartbeat sends a keep-alive comment to all SSE clients.
func (h *SSEHub) SendHeartbeat() {
	msg := ": heartbeat\n\n"
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}
