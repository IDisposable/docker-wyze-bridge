package webui

import (
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// handleHLSProxy proxies HLS requests from the browser through our bridge
// to go2rtc. This lets the browser make same-origin requests to the
// bridge (http://HOST:5080/hls/...) which avoids CORS entirely, and
// means the response goes through our CORS middleware on the way out.
//
// Mapped paths:
//
//	/hls/{cam}.m3u8           → http://localhost:1984/api/stream.m3u8?src={cam}   (master)
//	/hls/hls/playlist.m3u8?id → http://localhost:1984/api/hls/playlist.m3u8?id    (media)
//	/hls/hls/segment.ts?id    → http://localhost:1984/api/hls/segment.ts?id       (TS)
//	/hls/hls/init.mp4?id      → http://localhost:1984/api/hls/init.mp4?id         (fMP4)
//	/hls/hls/segment.m4s?id   → http://localhost:1984/api/hls/segment.m4s?id
//
// The master playlist returned by go2rtc references the media playlist as
// the relative URL "hls/playlist.m3u8?id=...". Clients resolve that against
// /hls/{cam}.m3u8, so the subsequent request comes in as /hls/hls/playlist.m3u8
// — we forward the whole remainder (after /hls/) to /api/ verbatim.
func (s *Server) handleHLSProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/hls/")
	if path == "" {
		http.Error(w, "camera name required", http.StatusBadRequest)
		return
	}

	// Entry-point request: /hls/{cam}.m3u8 → /api/stream.m3u8?src={cam}
	if strings.HasSuffix(path, ".m3u8") && !strings.Contains(path, "/") {
		camName := strings.TrimSuffix(path, ".m3u8")
		if s.camMgr.GetCamera(camName) == nil {
			http.NotFound(w, r)
			return
		}
		target := s.go2rtcAPI.BaseURL() + "/api/stream.m3u8?src=" + url.QueryEscape(camName)
		s.proxyGet(w, r, target)
		return
	}

	// Sub-playlist / segments: forward the remainder as-is under /api/.
	// The session id lives in the query string, which ReverseProxy preserves.
	target := s.go2rtcAPI.BaseURL() + "/api/" + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	s.proxyGet(w, r, target)
}

// proxyGet forwards a GET request to an upstream URL and streams the
// response body to the client, preserving Content-Type and related
// media headers. Used for HLS playlists and segments.
func (s *Server) proxyGet(w http.ResponseWriter, r *http.Request, upstream string) {
	u, err := url.Parse(upstream)
	if err != nil {
		http.Error(w, "bad upstream url", http.StatusInternalServerError)
		return
	}

	// Use httputil.ReverseProxy for proper streaming + header handling.
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.URL.Path = u.Path
			req.URL.RawQuery = u.RawQuery
			req.Host = u.Host
			// Strip Origin so go2rtc sees a same-origin request and
			// doesn't need to worry about CORS headers of its own.
			req.Header.Del("Origin")
			req.Header.Del("Referer")
		},
		ModifyResponse: func(resp *http.Response) error {
			// Our own CORS middleware will set Access-Control-* on the way
			// out. Strip any upstream CORS headers to avoid duplication.
			resp.Header.Del("Access-Control-Allow-Origin")
			resp.Header.Del("Access-Control-Allow-Methods")
			resp.Header.Del("Access-Control-Allow-Headers")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Warn().Err(err).Str("upstream", upstream).Msg("HLS proxy error")
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
	_ = io.Discard // keep import stable if ReverseProxy impl changes
}

// handleWSProxy proxies WebSocket upgrade requests from the browser to
// go2rtc. video-rtc.js negotiates WebRTC over a WebSocket at
// /api/ws?src=... — we expose it at /ws?src=... so the page at
// :5080 can open a same-origin WebSocket (avoids CORS + mixed-origin).
//
// Path: /ws?src={cam} → ws://localhost:1984/api/ws?src={cam}
func (s *Server) handleWSProxy(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	if src == "" {
		http.Error(w, "src query required", http.StatusBadRequest)
		return
	}
	if s.camMgr.GetCamera(src) == nil {
		http.NotFound(w, r)
		return
	}

	upstreamHost := strings.TrimPrefix(s.go2rtcAPI.BaseURL(), "http://")
	upstreamHost = strings.TrimPrefix(upstreamHost, "https://")

	// Dial go2rtc
	upstream, err := net.DialTimeout("tcp", upstreamHost, 5*time.Second)
	if err != nil {
		s.log.Warn().Err(err).Str("cam", src).Msg("WS proxy upstream dial failed")
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	// Hijack the client connection so we can shuttle raw bytes
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		s.log.Warn().Err(err).Msg("WS proxy hijack failed")
		return
	}

	// Rewrite the request line to the upstream path + forward
	req := r.Clone(r.Context())
	req.URL.Path = "/api/ws"
	req.URL.RawQuery = r.URL.RawQuery
	req.Host = upstreamHost
	req.RequestURI = ""
	req.Header.Del("Origin")

	// Write request headers to upstream as raw HTTP
	if err := req.Write(upstream); err != nil {
		upstream.Close()
		client.Close()
		return
	}

	s.log.Debug().Str("cam", src).Msg("WS proxy established")

	// Bidirectional copy until either side closes
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	upstream.Close()
	client.Close()
}
