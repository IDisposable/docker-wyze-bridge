package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
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
	body := map[string]interface{}{
		"status":  "ok",
		"version": s.version,
		"uptime":  uptime,
	}
	// Surfaces the active issue count so HA can drive a binary
	// sensor (config OK / problems) off /api/health.
	body["config_errors"] = s.issues.Count()
	if issueList := s.issues.List(); len(issueList) > 0 {
		body["issues"] = issueList
		body["status"] = "degraded"
	}
	writeJSON(w, body)
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

// handleAPIDiscover triggers a bridge-wide Wyze API rediscovery on
// demand (POST-only to keep it out of accidental prefetch/preview).
// The actual work runs asynchronously — we acknowledge and let
// runDiscover write its completion Event to the metrics log.
func (s *Server) handleAPIDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.onDiscoverReq == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "discover hook not wired")
		return
	}
	go s.onDiscoverReq(s.rootCtx)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAPICameraAction(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/cameras/{name} or /api/cameras/{name}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/api/cameras/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]

	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "camera name required")
		return
	}

	cam := s.camMgr.GetCamera(name)
	if cam == nil {
		writeJSONError(w, http.StatusNotFound, "camera not found")
		return
	}

	if len(parts) == 1 || parts[1] == "" {
		// GET /api/cameras/{name}
		writeJSON(w, cam.StatusJSON())
		return
	}

	action := parts[1]
	// reqCtx: synchronous handlers; cancelled on client disconnect.
	// s.rootCtx: spawned supervisors; cancelled on bridge shutdown.
	reqCtx := r.Context()

	switch {
	case action == "restart" && r.Method == "POST":
		s.camMgr.RestartStream(reqCtx, name)
		writeJSON(w, map[string]string{"status": "ok"})

	case action == "quality" && r.Method == "POST":
		var body struct {
			Quality string `json:"quality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if body.Quality != "hd" && body.Quality != "sd" {
			writeJSONError(w, http.StatusBadRequest, "quality must be 'hd' or 'sd'")
			return
		}
		if err := s.camMgr.SetQuality(reqCtx, name, body.Quality); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "quality": body.Quality})

	case action == "audio" && r.Method == "POST":
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		cam.SetAudioOn(body.Enabled)
		writeJSON(w, map[string]interface{}{"status": "ok", "audio": body.Enabled})

	case action == "snapshot" && r.Method == "POST":
		if s.onSnapReq == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "snapshot manager not wired")
			return
		}
		// Fire-and-forget: capture outlives the HTTP response.
		go s.onSnapReq(s.rootCtx, name)
		writeJSON(w, map[string]string{"status": "ok", "camera": name})

	case action == "record" && r.Method == "POST":
		if s.recMgr == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "recording manager not wired")
			return
		}
		var body struct {
			Action string `json:"action"` // "start" | "stop"
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // body optional; "start" default
		controller, ok := s.recMgr.(recordingController)
		if !ok {
			writeJSONError(w, http.StatusNotImplemented, "recording manager does not support start/stop")
			return
		}
		switch body.Action {
		case "stop":
			controller.Stop(name)
		default:
			// rootCtx: the recorder supervisor outlives this request.
			if err := controller.Start(s.rootCtx, name); err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, map[string]interface{}{
			"status":    "ok",
			"camera":    name,
			"recording": s.recMgr.IsRecording(name),
		})

	case action == "property" && r.Method == "POST":
		// Camera-side settings via the Wyze cloud: body
		// {"property":"hor_flip","value":"on"}. Same table MQTT's
		// set topics use (wyzeapi.CloudProperties).
		if s.cloud == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "Wyze API not wired")
			return
		}
		var body struct {
			Property string `json:"property"`
			Value    string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if _, known := wyzeapi.CloudProperties[body.Property]; !known {
			writeJSONError(w, http.StatusBadRequest, "unknown property")
			return
		}
		if _, _, ok := wyzeapi.ParsePropertyValue(body.Property, body.Value); !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid value for "+body.Property)
			return
		}
		published, err := s.cloud.SetNamedProperty(cam.GetInfo(), body.Property, body.Value)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		if evLog := s.Events(); evLog != nil {
			evLog.Record(Event{Kind: "command", Camera: name, Message: body.Property + " = " + body.Value})
		}
		if s.onPropertySet != nil {
			go s.onPropertySet(name, body.Property, published)
		}
		writeJSON(w, map[string]interface{}{"status": "ok", "property": body.Property, "value": published})

	case action == "reboot" && r.Method == "POST":
		// Reboot the camera itself through the Wyze cloud (the "restart"
		// run_action), then re-register the stream so go2rtc dials the
		// camera fresh when it's back. Distinct from "restart", which
		// only re-registers the stream.
		if s.cloud == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "Wyze API not wired")
			return
		}
		if err := s.cloud.RunAction(cam.GetInfo(), "restart"); err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		if evLog := s.Events(); evLog != nil {
			evLog.Record(Event{Kind: "command", Camera: name, Message: "reboot requested"})
		}
		// rootCtx: the reconnect outlives this request.
		go s.camMgr.RestartStream(s.rootCtx, name)
		writeJSON(w, map[string]string{"status": "ok"})

	case action == "pin" && r.Method == "POST":
		// Pin the camera's LAN address: body {"ip":"192.168.4.151"};
		// an empty ip unpins. Persisted by MAC; see camera.PinLanIP.
		var body struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		effective, err := s.camMgr.PinLanIP(s.rootCtx, name, body.IP)
		switch {
		case errors.Is(err, camera.ErrBadPinIP), errors.Is(err, camera.ErrNotLANCamera):
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if evLog := s.Events(); evLog != nil {
			msg := "address pinned to " + body.IP
			if strings.TrimSpace(body.IP) == "" {
				msg = "address unpinned (now " + effective + ")"
			}
			evLog.Record(Event{Kind: "lan_ip", Camera: name, Message: msg})
		}
		writeJSON(w, map[string]interface{}{"status": "ok", "ip": effective, "pinned_ip": cam.PinnedIP()})

	case action == "ignore" && r.Method == "POST":
		// Drops the camera from the registry for good (persisted by
		// MAC); /api/ignored/<mac>/restore brings it back.
		entry, err := s.camMgr.Ignore(reqCtx, name)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if evLog := s.Events(); evLog != nil {
			evLog.Record(Event{Kind: "ignore", Camera: name, Message: "ignored (" + entry.MAC + ")"})
		}
		writeJSON(w, map[string]interface{}{"status": "ok", "ignored": entry})

	default:
		writeJSONError(w, http.StatusNotFound, "not found")
	}
}

// handleAPIIgnored lists the operator's ignore list.
func (s *Server) handleAPIIgnored(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	list := s.camMgr.Ignored()
	if list == nil {
		list = []wyzeapi.IgnoredCamera{}
	}
	writeJSON(w, list)
}

// handleAPIIgnoredAction handles POST /api/ignored/{mac}/restore: takes
// the MAC off the list and kicks a rediscovery so the camera comes
// back without waiting for the next refresh. The discovery runs
// asynchronously like the Rediscover button's; the camera_added SSE
// event tells the dashboard when the card is back.
func (s *Server) handleAPIIgnoredAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/ignored/")
	parts := strings.SplitN(path, "/", 2)
	mac := parts[0]
	if mac == "" || len(parts) < 2 || parts[1] != "restore" {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	entry, err := s.camMgr.Unignore(mac)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if evLog := s.Events(); evLog != nil {
		evLog.Record(Event{Kind: "ignore", Camera: entry.Nickname, Message: "restored (" + entry.MAC + ")"})
	}
	if s.onDiscoverReq != nil {
		go s.onDiscoverReq(s.rootCtx)
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "restored": entry})
}

// recordingController is satisfied by recording.Manager. Kept as an
// inline interface (rather than in server.go) so the HTTP handler
// doesn't force the RecordingObserver interface to also expose mutation.
type recordingController interface {
	Start(ctx context.Context, camName string) error
	Stop(camName string)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/snapshot/")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "camera name required")
		return
	}

	go2rtc := s.go2rtc()
	if go2rtc == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "bridge still starting; go2rtc not yet ready")
		return
	}
	ctx := r.Context()
	jpeg, err := go2rtc.GetSnapshot(ctx, name)
	if err != nil {
		s.log.Warn().Err(err).Str("cam", name).Msg("snapshot failed")
		writeJSONError(w, http.StatusServiceUnavailable, "snapshot unavailable")
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
		fmt.Fprintf(w, "#EXTINF:-1,%s\n", cam.GetInfo().Nickname)
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
	fmt.Fprintf(w, "#EXTINF:-1,%s\n", cam.GetInfo().Nickname)
	fmt.Fprintf(w, "rtsp://%s:8554/%s\n", bridgeIP, cam.Name())
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// writeJSONError emits {"error":"..."} with the given status code.
// Use for /api/* handlers so clients get a consistent error shape.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
