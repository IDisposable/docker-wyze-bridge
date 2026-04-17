// Package webui provides the HTTP server, REST API, and static WebUI.
package webui

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

//go:embed static/*
var staticFS embed.FS

// SnapshotRequester triggers an on-demand capture-to-disk for a camera.
// Supplied by main.go via OnSnapshotRequest so the webui package stays
// decoupled from the snapshot package (which depends on us via SSE).
type SnapshotRequester func(ctx context.Context, camName string)

// Server is the WebUI HTTP server.
type Server struct {
	log       zerolog.Logger
	cfg       *config.Config
	camMgr    *camera.Manager
	go2rtcAPI *go2rtcmgr.APIClient
	sseHub    *SSEHub
	auth      *AuthMiddleware
	srv       *http.Server
	version   string
	startTime time.Time
	onSnapReq SnapshotRequester
	mars      MarsTokenMinter
}

// NewServer creates a new WebUI server.
func NewServer(
	cfg *config.Config,
	camMgr *camera.Manager,
	go2rtcAPI *go2rtcmgr.APIClient,
	version string,
	log zerolog.Logger,
) *Server {
	s := &Server{
		log:       log,
		cfg:       cfg,
		camMgr:    camMgr,
		go2rtcAPI: go2rtcAPI,
		sseHub:    NewSSEHub(log),
		version:   version,
		startTime: time.Now(),
	}

	s.auth = NewAuthMiddleware(
		cfg.BridgeAuth,
		cfg.BridgeUsername,
		cfg.BridgePassword,
		cfg.BridgeAPIToken,
	)

	return s
}

// SSE returns the SSE hub for sending events.
func (s *Server) SSE() *SSEHub {
	return s.sseHub
}

// OnSnapshotRequest registers a callback that fires when the WebUI's
// snapshot button is clicked. main.go wires this to snapMgr.CaptureOne.
// Nil is safe — the button just returns 503 until the hook is attached.
func (s *Server) OnSnapshotRequest(fn SnapshotRequester) {
	s.onSnapReq = fn
}

// StartTime returns when the server was created.
func (s *Server) StartTime() time.Time {
	return s.startTime
}

// Start begins serving HTTP.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.cfg.BridgePort),
		Handler:      s.corsMiddleware(s.logMiddleware(mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	s.log.Info().Int("port", s.cfg.BridgePort).Msg("WebUI server starting")
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.sseHub.Close()
	return s.srv.Shutdown(ctx)
}

// logMiddleware wraps a handler with request logging.
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		s.log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rw.status).
			Dur("duration", time.Since(start)).
			Msg("http request")
	})
}

// corsMiddleware adds permissive CORS headers so the WebUI can be
// called from other origins (e.g. Home Assistant dashboards embedding
// a camera card, or direct API use from other tools).
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush passes through to the underlying ResponseWriter's Flusher.
// Required for SSE — without this, the type assertion `w.(http.Flusher)`
// in the SSE handler fails and returns 500.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack passes through to the underlying ResponseWriter's Hijacker.
// Required for WebSocket upgrade — without this, the type assertion
// `w.(http.Hijacker)` in the WS proxy fails and the connection dies.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// Pages
	mux.HandleFunc("/", s.auth.Wrap(s.handleIndex))
	mux.HandleFunc("/camera/", s.auth.Wrap(s.handleCameraPage))

	// Favicon (served from embedded icon.png, no auth required)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)

	// REST API
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/cameras", s.auth.Wrap(s.handleAPICameras))
	mux.HandleFunc("/api/cameras/", s.auth.Wrap(s.handleAPICameraAction))
	mux.HandleFunc("/api/snapshot/", s.auth.Wrap(s.handleSnapshot))
	mux.HandleFunc("/api/streams", s.auth.Wrap(s.handleStreamsM3U8))
	mux.HandleFunc("/api/streams/", s.auth.Wrap(s.handleStreamM3U8))

	// SSE
	mux.HandleFunc("/events", s.auth.Wrap(s.sseHub.ServeHTTP))

	// HLS proxy — forwards /hls/{cam}.m3u8 + segments through our bridge
	// so the browser makes a same-origin request (no CORS preflight) and
	// our CORS middleware controls the response headers.
	mux.HandleFunc("/hls/", s.auth.Wrap(s.handleHLSProxy))

	// WebRTC/MSE WebSocket proxy — /ws?src={cam} → go2rtc /api/ws?src={cam}.
	// Used by <video-rtc> on both the grid and detail pages. Holding this
	// socket open keeps the go2rtc producer alive, which is what lets the
	// grid show live video without reconnect churn.
	mux.HandleFunc("/ws", s.auth.Wrap(s.handleWSProxy))

	// Backward-compat aliases
	mux.HandleFunc("/cams.m3u8", s.auth.Wrap(s.handleStreamsM3U8))
	mux.HandleFunc("/stream/", s.auth.Wrap(s.handleStreamM3U8))

	// wyze-shim for the gwell-proxy sidecar. Loopback-only so Mars
	// credentials can't leak to the LAN. See internal/webui/shim.go.
	mux.HandleFunc("/internal/wyze/Camera/CameraList", requireLoopback(s.handleShimCameraList))
	mux.HandleFunc("/internal/wyze/Camera/DeviceInfo", requireLoopback(s.handleShimDeviceInfo))
	mux.HandleFunc("/internal/wyze/Camera/CameraToken", requireLoopback(s.handleShimCameraToken))
}
