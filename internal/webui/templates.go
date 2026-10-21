package webui

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
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
		Video       string // live | idle | no_video | unknown (see camera.Video)
		VideoTitle  string // tooltip: last error / last video time
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
			Name:       name,
			Nickname:   snap.Info.Nickname,
			Model:      snap.Info.Model,
			ModelName:  snap.Info.ModelName(),
			State:      snap.State.String(),
			Video:      snap.Video.String(),
			VideoTitle: videoTitle(snap),
			Quality:    snap.Quality,
			IP:         snap.Info.LanIP,
			Recording:  recording,
			RTSPURL:    template.URL(fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name)),
			// Absolute URL through our bridge so it's usable when copied into
			// an external HLS player. Relative paths work in-browser too but
			// break when pasted elsewhere.
			HLSURL:      fmt.Sprintf("http://%s:%d/hls/%s.m3u8", bridgeIP, s.cfg.BridgePort, name),
			WebRTCURL:   fmt.Sprintf("http://%s:1984/api/webrtc?src=%s", bridgeIP, name),
			SnapshotURL: fmt.Sprintf("%s/api/snapshot/%s", basePath, name),
			Go2RTCURL:   fmt.Sprintf("%s/ws?src=%s", basePath, name),
		})
	}

	tmpl, err := template.New("index").Funcs(templateFuncs).Parse(indexHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		s.log.Error().Err(err).Msg("index template parse error")
		return
	}

	// Ignored cameras get their own strip under the grid so an
	// accidental click is one "Restore" away from undone.
	type ignoredData struct {
		MAC      string
		Nickname string
		Model    string
		Since    string
	}
	var ignored []ignoredData
	for _, ic := range s.camMgr.Ignored() {
		nick := ic.Nickname
		if nick == "" {
			nick = ic.MAC
		}
		ignored = append(ignored, ignoredData{
			MAC:      ic.MAC,
			Nickname: nick,
			Model:    wyzeapi.CameraInfo{Model: ic.Model}.ModelName(),
			Since:    ic.Since.Local().Format("2006-01-02 15:04"),
		})
	}

	data := map[string]interface{}{
		"Version":  s.version,
		"Cameras":  cams,
		"Ignored":  ignored,
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
		"Version":    s.version,
		"BasePath":   basePath,
		"Name":       name,
		"Nickname":   snap.Info.Nickname,
		"Model":      snap.Info.Model,
		"ModelName":  snap.Info.ModelName(),
		"State":      snap.State.String(),
		"Video":      snap.Video.String(),
		"VideoTitle": videoTitle(snap),
		"LastVideo":  formatWhen(snap.LastVideo),
		"LastError":  formatError(snap),
		"Quality":    snap.Quality,
		"Audio":      snap.AudioOn,
		"Recording":  recording,
		"IP":         snap.Info.LanIP,
		"MAC":        snap.Info.MAC,
		"FWVersion":  snap.Info.FWVersion,
		"RTSPURL":    template.URL(fmt.Sprintf("rtsp://%s:8554/%s", bridgeIP, name)),
		"HLSURL":     fmt.Sprintf("http://%s:%d/hls/%s.m3u8", bridgeIP, s.cfg.BridgePort, name),
		"Go2RTCURL":  fmt.Sprintf("%s/ws?src=%s", basePath, name),
		"BridgeIP":   bridgeIP,
	}

	tmpl, err := template.New("camera").Funcs(templateFuncs).Parse(cameraHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// templateFuncs are the helpers the embedded pages use.
var templateFuncs = template.FuncMap{
	"videoLabel": videoLabel,
}

// videoLabel is the badge text for a camera.Video string. "unknown"
// renders empty so cameras that aren't streaming show no second badge.
func videoLabel(video string) string {
	switch video {
	case "live":
		return "live"
	case "idle":
		return "idle"
	case "no_video":
		return "no video"
	default:
		return ""
	}
}

// videoTitle is the tooltip behind the video badge: the last go2rtc
// error with its age, or when video last flowed.
func videoTitle(snap camera.Snapshot) string {
	switch {
	case snap.LastError != "":
		return formatError(snap)
	case !snap.LastVideo.IsZero():
		return "last video " + formatWhen(snap.LastVideo)
	case snap.Video == camera.VideoIdle:
		return "nobody is pulling this stream"
	default:
		return ""
	}
}

// formatWhen renders a timestamp as a short relative age, or "never".
func formatWhen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

// formatError renders the last stream error with its age, or "none".
func formatError(snap camera.Snapshot) string {
	if snap.LastError == "" {
		return "none"
	}
	return snap.LastError + " (" + formatWhen(snap.LastErrorAt) + ")"
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
                    <video-rtc src="{{.Go2RTCURL}}" data-poster="{{.SnapshotURL}}" muted></video-rtc>
                    {{else}}
                    <img src="{{.SnapshotURL}}" alt="{{.Nickname}}" loading="lazy"
                         onerror="this.style.display='none'">
                    {{end}}
                    <span class="state-badge {{.State}}">{{.State}}</span>
                    <span class="video-badge {{.Video}}" data-cam="{{.Name}}" title="{{.VideoTitle}}">{{videoLabel .Video}}</span>
                    <a class="camera-preview-overlay" href="{{$.BasePath}}/camera/{{.Name}}" aria-label="{{.Nickname}} details"></a>
                </div>
                <a href="{{$.BasePath}}/camera/{{.Name}}" class="camera-info">
                    <h3>{{.Nickname}}</h3>
                    <p>{{.ModelName}} &middot; {{.Quality}} &middot; <span class="cam-ip">{{.IP}}</span></p>
                </a>
                <div class="camera-links">
                    <button type="button" class="copy-btn" data-url="{{.RTSPURL}}" title="Click to copy RTSP URL (paste into VLC or ffmpeg)">RTSP</button>
                    <button type="button" class="copy-btn" data-url="{{.HLSURL}}" title="Click to copy HLS URL (paste into VLC or an HLS player — Chrome/Firefox can't play HLS natively)">HLS</button>
                    <button type="button" class="snap-btn" data-cam="{{.Name}}" title="Take snapshot (saves to SNAPSHOT_PATH)" aria-label="Take snapshot">📷</button>
                    <button type="button" class="record-btn {{if .Recording}}on{{end}}" data-cam="{{.Name}}" data-recording="{{.Recording}}" title="{{if .Recording}}Stop recording{{else}}Start recording{{end}}" aria-label="{{if .Recording}}Stop recording{{else}}Start recording{{end}}">{{if .Recording}}⏹{{else}}⏺{{end}}</button>
                    <button type="button" class="ignore-btn" data-cam="{{.Name}}" data-nickname="{{.Nickname}}" title="Ignore this camera (stops reconnecting, hides it; restore from the list below)" aria-label="Ignore camera">🚫</button>
                </div>
            </div>
        {{else}}
            <div class="no-cameras">
                <p>No cameras found. Check your Wyze credentials and network connectivity.</p>
            </div>
        {{end}}
        </div>
        {{if .Ignored}}
        <section class="ignored" id="ignored">
            <h2>Ignored cameras <small>not connected, not shown, not retried</small></h2>
            <table>
                <tr><th>Camera</th><th>Model</th><th>MAC</th><th>Since</th><th></th></tr>
                {{range .Ignored}}
                <tr data-mac="{{.MAC}}">
                    <td>{{.Nickname}}</td>
                    <td>{{.Model}}</td>
                    <td><code>{{.MAC}}</code></td>
                    <td>{{.Since}}</td>
                    <td><button type="button" class="restore-btn" data-mac="{{.MAC}}" title="Stop ignoring and rediscover">Restore</button></td>
                </tr>
                {{end}}
            </table>
        </section>
        {{end}}
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
                <tr><td>State</td><td><span class="state-badge {{.State}}">{{.State}}</span> <span class="video-badge {{.Video}}" data-cam="{{.Name}}" title="{{.VideoTitle}}">{{videoLabel .Video}}</span></td></tr>
                <tr><td>Last video</td><td class="cam-last-video" data-cam="{{.Name}}">{{.LastVideo}}</td></tr>
                <tr><td>Last error</td><td class="cam-last-error" data-cam="{{.Name}}">{{.LastError}}</td></tr>
                <tr><td>Quality</td><td id="quality">{{.Quality}}</td></tr>
                <tr><td>Audio</td><td>{{.Audio}}</td></tr>
                <tr><td>IP</td><td class="cam-ip" data-cam="{{.Name}}">{{.IP}}</td></tr>
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
