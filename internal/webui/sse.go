package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/rs/zerolog"
)

// SSEHub manages Server-Sent Events connections.
type SSEHub struct {
	log     zerolog.Logger
	clients map[chan string]struct{}
	mu      sync.RWMutex
	closed  bool
}

// NewSSEHub creates a new SSE hub.
func NewSSEHub(log zerolog.Logger) *SSEHub {
	return &SSEHub{
		log:     log,
		clients: make(map[chan string]struct{}),
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

// Send broadcasts an SSE event to all connected clients.
func (h *SSEHub) Send(event, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.clients {
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
