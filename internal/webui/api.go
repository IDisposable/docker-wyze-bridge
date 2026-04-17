package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleFavicon serves the embedded icon.png as the tab favicon.
// Browsers accept PNG as favicon even when they request /favicon.ico.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/icon.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := int(time.Since(s.startTime).Seconds())
	writeJSON(w, map[string]interface{}{
		"status":  "ok",
		"version": s.version,
		"uptime":  uptime,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"version":        s.version,
		"go2rtc_version": "1.9.14",
	})
}

func (s *Server) handleAPICameras(w http.ResponseWriter, r *http.Request) {
	cameras := s.camMgr.Cameras()
	result := make([]map[string]interface{}, 0, len(cameras))
	for _, cam := range cameras {
		result = append(result, cam.StatusJSON())
	}
	writeJSON(w, result)
}

func (s *Server) handleAPICameraAction(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/cameras/{name} or /api/cameras/{name}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/api/cameras/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]

	if name == "" {
		http.Error(w, "camera name required", http.StatusBadRequest)
		return
	}

	cam := s.camMgr.GetCamera(name)
	if cam == nil {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 || parts[1] == "" {
		// GET /api/cameras/{name}
		writeJSON(w, cam.StatusJSON())
		return
	}

	action := parts[1]
	ctx := context.Background()

	switch {
	case action == "restart" && r.Method == "POST":
		s.camMgr.RestartStream(ctx, name)
		writeJSON(w, map[string]string{"status": "ok"})

	case action == "quality" && r.Method == "POST":
		var body struct {
			Quality string `json:"quality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Quality != "hd" && body.Quality != "sd" {
			http.Error(w, "quality must be 'hd' or 'sd'", http.StatusBadRequest)
			return
		}
		if err := s.camMgr.SetQuality(ctx, name, body.Quality); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "quality": body.Quality})

	case action == "audio" && r.Method == "POST":
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		cam.AudioOn = body.Enabled
		writeJSON(w, map[string]interface{}{"status": "ok", "audio": body.Enabled})

	case action == "snapshot" && r.Method == "POST":
		if s.onSnapReq == nil {
			http.Error(w, "snapshot manager not wired", http.StatusServiceUnavailable)
			return
		}
		// Fire-and-forget — capture can take up to the go2rtc snapshot
		// timeout, don't block the HTTP response on it.
		go s.onSnapReq(context.Background(), name)
		writeJSON(w, map[string]string{"status": "ok", "camera": name})

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/snapshot/")
	if name == "" {
		http.Error(w, "camera name required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	jpeg, err := s.go2rtcAPI.GetSnapshot(ctx, name)
	if err != nil {
		s.log.Warn().Err(err).Str("cam", name).Msg("snapshot failed")
		http.Error(w, "snapshot unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(jpeg)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.renderIndex(w, r)
}

func (s *Server) handleCameraPage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/camera/")
	if name == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	cam := s.camMgr.GetCamera(name)
	if cam == nil {
		http.NotFound(w, r)
		return
	}
	s.renderCamera(w, r, cam)
}

func (s *Server) handleStreamsM3U8(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")

	fmt.Fprintln(w, "#EXTM3U")
	fmt.Fprintln(w, "#EXT-X-VERSION:3")

	bridgeIP := s.displayHost(r)
	for _, cam := range s.camMgr.Cameras() {
		fmt.Fprintf(w, "#EXTINF:-1,%s\n", cam.Info.Nickname)
		fmt.Fprintf(w, "rtsp://%s:8554/%s\n", bridgeIP, cam.Name())
	}
}

func (s *Server) handleStreamM3U8(w http.ResponseWriter, r *http.Request) {
	// /api/streams/{name}.m3u8 or /stream/{name}.m3u8
	path := r.URL.Path
	path = strings.TrimPrefix(path, "/api/streams/")
	path = strings.TrimPrefix(path, "/stream/")
	name := strings.TrimSuffix(path, ".m3u8")

	cam := s.camMgr.GetCamera(name)
	if cam == nil {
		http.NotFound(w, r)
		return
	}

	bridgeIP := s.displayHost(r)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	fmt.Fprintln(w, "#EXTM3U")
	fmt.Fprintln(w, "#EXT-X-VERSION:3")
	fmt.Fprintf(w, "#EXTINF:-1,%s\n", cam.Info.Nickname)
	fmt.Fprintf(w, "rtsp://%s:8554/%s\n", bridgeIP, cam.Name())
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
