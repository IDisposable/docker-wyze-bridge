package webui

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
)

// ingressBasePath returns the HA ingress path prefix from the
// X-Ingress-Path header, or "" when running standalone. The returned
// value never has a trailing slash so it can be prepended directly to
// absolute paths: basePath + "/static/app.js".
func ingressBasePath(r *http.Request) string {
	p := r.Header.Get("X-Ingress-Path")
	return strings.TrimRight(p, "/")
}

// displayHost picks the hostname used to build stream URLs shown to the user.
// We prefer r.Host (whatever the browser is currently connected to) so the
// copy-to-clipboard URLs route back the same way. BRIDGE_IP is only a
// fallback — its real job is being advertised as a WebRTC ICE candidate,
// not driving UI links. Without this, browsing at localhost (WSL2 dev)
// showed a LAN IP that wasn't reachable from the host.
func (s *Server) displayHost(r *http.Request) string {
	h := r.Host
	if idx := strings.Index(h, ":"); idx >= 0 {
		h = h[:idx]
	}
	if h != "" {
		return h
	}
	return s.cfg.BridgeIP
}

// renderIndex renders the camera grid page.
func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request) {
	cameras := s.camMgr.Cameras()
	bridgeIP := s.displayHost(r)
	basePath := ingressBasePath(r)

	type camData struct {
		Name        string
		Nickname    string
		Model       string
		ModelName   string
		State       string
		Quality     string
		IP          string
		Recording   bool
		RTSPURL     template.URL // rtsp:// — marked safe so html/template doesn't replace with ZgotmplZ
		HLSURL      string
		WebRTCURL   string
		SnapshotURL string
		Go2RTCURL   string
	}

	var cams []camData
	for _, cam := range cameras {
		name := cam.Name()
		recording := false
		if s.recMgr != nil {
			recording = s.recMgr.IsRecording(name)
		}
		snap := cam.Snapshot()
		cams = append(cams, camData{
			Name:      name,
			Nickname:  snap.Info.Nickname,
			Model:     snap.Info.Model,
			ModelName: snap.Info.ModelName(),
			State:     snap.State.String(),
			Quality:   snap.Quality,
			IP:        snap.Info.LanIP,
			Recording: recording,
			RTSPURL:   template.URL(fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name)),
			// Absolute URL through our bridge so it's usable when copied into
			// an external HLS player. Relative paths work in-browser too but
			// break when pasted elsewhere.
			HLSURL:      fmt.Sprintf("http://%s:%d/hls/%s.m3u8", bridgeIP, s.cfg.BridgePort, name),
			WebRTCURL:   fmt.Sprintf("http://%s:1984/api/webrtc?src=%s", bridgeIP, name),
			SnapshotURL: fmt.Sprintf("%s/api/snapshot/%s", basePath, name),
			Go2RTCURL:   fmt.Sprintf("%s/ws?src=%s", basePath, name),
		})
	}

	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		s.log.Error().Err(err).Msg("index template parse error")
		return
	}

	data := map[string]interface{}{
		"Version":  s.version,
		"Cameras":  cams,
		"Uptime":   int(time.Since(s.startTime).Seconds()),
		"BasePath": basePath,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		s.log.Error().Err(err).Msg("index template execute error")
	}
}

// renderCamera renders the single camera detail page.
func (s *Server) renderCamera(w http.ResponseWriter, r *http.Request, cam *camera.Camera) {
	bridgeIP := s.displayHost(r)
	name := cam.Name()
	basePath := ingressBasePath(r)
	recording := false
	if s.recMgr != nil {
		recording = s.recMgr.IsRecording(name)
	}
	snap := cam.Snapshot()
	data := map[string]interface{}{
		"Version":   s.version,
		"BasePath":  basePath,
		"Name":      name,
		"Nickname":  snap.Info.Nickname,
		"Model":     snap.Info.Model,
		"ModelName": snap.Info.ModelName(),
		"State":     snap.State.String(),
		"Quality":   snap.Quality,
		"Audio":     snap.AudioOn,
		"Recording": recording,
		"IP":        snap.Info.LanIP,
		"MAC":       snap.Info.MAC,
		"FWVersion": snap.Info.FWVersion,
		"RTSPURL":   template.URL(fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name)),
		"HLSURL":    fmt.Sprintf("http://%s:%d/hls/%s.m3u8", bridgeIP, s.cfg.BridgePort, name),
		"Go2RTCURL": fmt.Sprintf("%s/ws?src=%s", basePath, name),
		"BridgeIP":  bridgeIP,
	}

	tmpl, err := template.New("camera").Parse(cameraHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Wyze Bridge</title>
    <link rel="icon" type="image/png" href="{{.BasePath}}/favicon.ico">
    <link rel="stylesheet" href="{{.BasePath}}/static/style.css">
    <script>window.__BASE_PATH = '{{.BasePath}}';</script>
    <script type="module" src="{{.BasePath}}/static/video-stream.js"></script>
</head>
<body>
    <header>
        <h1>Wyze Bridge <span class="version">v{{.Version}}</span></h1>
        <nav class="header-nav">
            <a href="{{.BasePath}}/metrics" title="Metrics, issues, events">Metrics</a>
        </nav>
        <button type="button" id="discover-btn" class="discover-btn" title="Re-poll Wyze API for added/removed cameras">↻ Rediscover</button>
    </header>
    <main>
        <div class="camera-grid" id="camera-grid">
        {{range .Cameras}}
            <div class="camera-card" data-cam="{{.Name}}" data-state="{{.State}}">
                <div class="camera-preview">
                    {{if eq .State "streaming"}}
                    <video-rtc src="{{.Go2RTCURL}}" data-poster="{{.SnapshotURL}}"></video-rtc>
                    {{else}}
                    <img src="{{.SnapshotURL}}" alt="{{.Nickname}}" loading="lazy"
                         onerror="this.style.display='none'">
                    {{end}}
                    <span class="state-badge {{.State}}">{{.State}}</span>
                    <a class="camera-preview-overlay" href="{{$.BasePath}}/camera/{{.Name}}" aria-label="{{.Nickname}} details"></a>
                </div>
                <a href="{{$.BasePath}}/camera/{{.Name}}" class="camera-info">
                    <h3>{{.Nickname}}</h3>
                    <p>{{.ModelName}} &middot; {{.Quality}} &middot; {{.IP}}</p>
                </a>
                <div class="camera-links">
                    <button type="button" class="copy-btn" data-url="{{.RTSPURL}}" title="Click to copy RTSP URL (paste into VLC or ffmpeg)">RTSP</button>
                    <button type="button" class="copy-btn" data-url="{{.HLSURL}}" title="Click to copy HLS URL (paste into VLC or an HLS player — Chrome/Firefox can't play HLS natively)">HLS</button>
                    <button type="button" class="snap-btn" data-cam="{{.Name}}" title="Take snapshot (saves to SNAPSHOT_PATH)" aria-label="Take snapshot">📷</button>
                    <button type="button" class="record-btn {{if .Recording}}on{{end}}" data-cam="{{.Name}}" data-recording="{{.Recording}}" title="{{if .Recording}}Stop recording{{else}}Start recording{{end}}" aria-label="{{if .Recording}}Stop recording{{else}}Start recording{{end}}">{{if .Recording}}⏹{{else}}⏺{{end}}</button>
                </div>
            </div>
        {{else}}
            <div class="no-cameras">
                <p>No cameras found. Check your Wyze credentials and network connectivity.</p>
            </div>
        {{end}}
        </div>
    </main>
    <script src="{{.BasePath}}/static/app.js"></script>
</body>
</html>`

const cameraHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Nickname}} - Wyze Bridge</title>
    <link rel="icon" type="image/png" href="{{.BasePath}}/favicon.ico">
    <link rel="stylesheet" href="{{.BasePath}}/static/style.css">
    <script>window.__BASE_PATH = '{{.BasePath}}';</script>
    <script type="module" src="{{.BasePath}}/static/video-stream.js"></script>
</head>
<body>
    <header>
        <a href="{{.BasePath}}/" class="back">&larr; All Cameras</a>
        <h1>{{.Nickname}} <span class="version">v{{.Version}}</span></h1>
        <nav class="header-nav">
            <a href="{{.BasePath}}/metrics" title="Metrics, issues, events">Metrics</a>
        </nav>
    </header>
    <main class="camera-detail">
        <div class="player-container">
            <video-rtc src="{{.Go2RTCURL}}"></video-rtc>
        </div>
        <div class="camera-meta">
            <table>
                <tr><td>Model</td><td>{{.ModelName}} ({{.Model}})</td></tr>
                <tr><td>State</td><td><span class="state-badge {{.State}}">{{.State}}</span></td></tr>
                <tr><td>Quality</td><td id="quality">{{.Quality}}</td></tr>
                <tr><td>Audio</td><td>{{.Audio}}</td></tr>
                <tr><td>IP</td><td>{{.IP}}</td></tr>
                <tr><td>MAC</td><td>{{.MAC}}</td></tr>
                <tr><td>Firmware</td><td>{{.FWVersion}}</td></tr>
            </table>
            <div class="stream-urls">
                <h3>Stream URLs <small>(click to copy)</small></h3>
                <code class="copy-btn" data-url="{{.RTSPURL}}" title="Click to copy">{{.RTSPURL}}</code>
                <code class="copy-btn" data-url="{{.HLSURL}}" title="Click to copy">{{.HLSURL}}</code>
            </div>
            <div class="actions">
                <button onclick="restartStream('{{.Name}}')">Restart Stream</button>
                <button onclick="setQuality('{{.Name}}', 'hd')">HD</button>
                <button onclick="setQuality('{{.Name}}', 'sd')">SD</button>
                <button type="button" class="snap-btn" data-cam="{{.Name}}" title="Take snapshot (saves to SNAPSHOT_PATH)">📷 Snapshot</button>
                <button type="button" class="record-btn {{if .Recording}}on{{end}}" data-cam="{{.Name}}" data-recording="{{.Recording}}">{{if .Recording}}⏹ Stop Recording{{else}}⏺ Start Recording{{end}}</button>
            </div>
        </div>
    </main>
    <script src="{{.BasePath}}/static/app.js"></script>
</body>
</html>`
