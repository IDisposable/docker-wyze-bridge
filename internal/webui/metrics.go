package webui

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/issues"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// MetricsSnapshot is the full JSON payload served at /api/metrics.
// Every field is pure data; the metrics.html page renders it.
type MetricsSnapshot struct {
	Bridge   BridgeSummary             `json:"bridge"`
	Issues   []issues.Issue            `json:"issues,omitempty"`
	Cameras  []CameraMetric            `json:"cameras"`
	WyzeAPI  []wyzeapi.EndpointStats   `json:"wyzeapi,omitempty"`
	Storage  *StorageSummary           `json:"storage,omitempty"`
	Events   []Event                   `json:"events,omitempty"`
}

type BridgeSummary struct {
	Version        string    `json:"version"`
	Uptime         int       `json:"uptime_s"`
	StartedAt      time.Time `json:"started_at"`
	CameraCount    int       `json:"camera_count"`
	StreamingCount int       `json:"streaming_count"`
	ErrorCount     int       `json:"error_count"`
	SSEClients     int       `json:"sse_clients"`
}

type CameraMetric struct {
	Name      string `json:"name"`
	Nickname  string `json:"nickname"`
	Model     string `json:"model"`
	ModelName string `json:"model_name"`
	Protocol  string `json:"protocol"` // "tutk" | "gwell" | "webrtc"
	// ProtocolForced is true when the current Protocol reflects a
	// runtime TUTK→WebRTC auto-fallback rather than the model
	// registry's default. Lets operators tell "Wyze pushed a bad FW"
	// from "we always ran it this way." Omitted from JSON when false.
	ProtocolForced bool   `json:"protocol_forced,omitempty"`
	State          string `json:"state"`
	Quality        string `json:"quality"`
	AudioOn        bool   `json:"audio"`
	ErrorCount     int    `json:"error_count"`
	Recording      bool   `json:"recording"`
	SessionBytes   int64  `json:"session_bytes,omitempty"`
}

type StorageSummary struct {
	RecordingsTotalBytes int64            `json:"recordings_total_bytes"`
	RecordingsPerCamera  map[string]int64 `json:"recordings_per_camera,omitempty"`
	LastRefresh          time.Time        `json:"last_refresh"`
}

// handleMetricsJSON serves the full snapshot.
func (s *Server) handleMetricsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildMetricsSnapshot())
}

// handleMetricsPage serves the human-readable HTML metrics page.
// Templates live in templates.go (embedded) — here we just execute.
func (s *Server) handleMetricsPage(w http.ResponseWriter, r *http.Request) {
	data := metricsPageData{
		BasePath:        ingressBasePath(r),
		MetricsSnapshot: s.buildMetricsSnapshot(),
	}
	tmpl := metricsTemplate()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		s.log.Error().Err(err).Msg("metrics template")
	}
}

// metricsPageData decorates the snapshot with the HA ingress base path
// so links inside the template (Cameras, Prometheus, JSON) resolve
// inside the addon's iframe instead of escaping to Home Assistant's
// root. Snapshot fields stay accessible at the template's top level
// via struct embedding — no existing {{.Bridge}} / {{.Issues}} etc.
// template bindings need to change.
type metricsPageData struct {
	BasePath string
	MetricsSnapshot
}

// buildMetricsSnapshot gathers a read-only view across every injected
// source. Nil sources are skipped — the page renders what it has.
func (s *Server) buildMetricsSnapshot() MetricsSnapshot {
	snap := MetricsSnapshot{}
	snap.Bridge = BridgeSummary{
		Version:    s.version,
		Uptime:     int(time.Since(s.startTime).Seconds()),
		StartedAt:  s.startTime,
		SSEClients: s.sseHub.ClientCount(),
	}

	cameras := s.camMgr.Cameras()
	snap.Bridge.CameraCount = len(cameras)

	snap.Cameras = make([]CameraMetric, 0, len(cameras))
	for _, cam := range cameras {
		// Single consistent snapshot per camera. Avoids tearing
		// against UpdateInfo / SetQuality / SetAudioOn while we
		// pluck multiple fields for the render.
		cs := cam.Snapshot()
		state := cs.State.String()
		switch state {
		case "streaming":
			snap.Bridge.StreamingCount++
		case "error":
			snap.Bridge.ErrorCount++
		}
		cm := CameraMetric{
			Name:           cam.Name(),
			Nickname:       cs.Info.Nickname,
			Model:          cs.Info.Model,
			ModelName:      cs.Info.ModelName(),
			Protocol:       protocolFor(cs.Info, cs.ForceWebRTC),
			ProtocolForced: cs.ForceWebRTC,
			State:          state,
			Quality:        cs.Quality,
			AudioOn:        cs.AudioOn,
			ErrorCount:     cs.ErrorCount,
		}
		if s.recMgr != nil {
			cm.Recording = s.recMgr.IsRecording(cam.Name())
			if cm.Recording {
				cm.SessionBytes = s.recMgr.SessionBytes(cam.Name())
			}
		}
		snap.Cameras = append(snap.Cameras, cm)
	}

	snap.Issues = s.issues.List()
	if s.apiStats != nil {
		snap.WyzeAPI = s.apiStats.EndpointStats()
	}
	if s.storage != nil {
		snap.Storage = &StorageSummary{
			RecordingsTotalBytes: s.storage.TotalBytes(),
			RecordingsPerCamera:  s.storage.PerCamera(),
			LastRefresh:          s.storage.LastRefresh(),
		}
	}
	if s.events != nil {
		snap.Events = s.events.Snapshot()
	}
	return snap
}

// protocolFor matches camera.Manager.streamSourceFor's logic without
// importing camera (it already imports wyzeapi). Keeps the view layer
// from having to know the streamSourceFor contract. forceWebRTC
// reflects the per-camera runtime auto-fallback state.
func protocolFor(info wyzeapi.CameraInfo, forceWebRTC bool) string {
	switch {
	case forceWebRTC || info.IsWebRTCStreamer():
		return "webrtc"
	case info.IsGwell():
		return "gwell"
	default:
		return "tutk"
	}
}

// formatBytes renders a byte count as a human-readable string for the
// HTML template. Not used by the JSON API; kept here so templates can
// call it.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatMs(d time.Duration) string {
	ms := float64(d.Microseconds()) / 1000.0
	return fmt.Sprintf("%.1f ms", ms)
}

func formatUptime(s int) string {
	d := s / 86400
	h := (s % 86400) / 3600
	m := (s % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// metricsTemplate returns the parsed template used by handleMetricsPage.
// Kept as a function (not a package-level var) so the funcMap is built
// once per request — negligible cost, keeps the package init free.
func metricsTemplate() *template.Template {
	funcs := template.FuncMap{
		"bytes":   formatBytes,
		"ms":      formatMs,
		"uptime":  formatUptime,
		"sinceMs": func(t time.Time) string { return formatMs(time.Since(t)) },
	}
	return template.Must(template.New("metrics").Funcs(funcs).Parse(metricsHTML))
}

const metricsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Bridge Metrics — wyze-bridge</title>
<meta http-equiv="refresh" content="10">
<style>
  :root { color-scheme: light dark; font-family: system-ui, sans-serif; }
  body  { max-width: 1200px; margin: 1.5rem auto; padding: 0 1rem; }
  h1    { margin: 0.2rem 0 1rem; }
  h2    { margin-top: 2rem; border-bottom: 1px solid #888; padding-bottom: 0.2rem; }
  table { border-collapse: collapse; width: 100%; font-size: 0.9rem; }
  th, td { text-align: left; padding: 0.3rem 0.6rem; border-bottom: 1px solid rgba(128,128,128,0.2); }
  th    { font-weight: 600; }
  .summary { display: flex; flex-wrap: wrap; gap: 1.5rem; margin-bottom: 1rem; }
  .summary div { min-width: 110px; }
  .summary b { display: block; font-size: 1.4rem; }
  .issue-err  { color: #c0392b; }
  .issue-warn { color: #d68910; }
  .pill { padding: 0.1rem 0.5rem; border-radius: 999px; font-size: 0.8rem; }
  .state-streaming   { background: #2ecc7133; color: #27ae60; }
  .state-connecting  { background: #f39c1233; color: #d68910; }
  .state-offline     { background: #bdc3c733; color: #7f8c8d; }
  .state-error       { background: #e74c3c33; color: #c0392b; }
  .state-discovering { background: #3498db33; color: #2980b9; }
  .muted { color: #888; font-size: 0.85rem; }
  details summary { cursor: pointer; padding: 0.3rem 0; font-weight: 600; }
  code { background: rgba(128,128,128,0.15); padding: 0 0.3rem; border-radius: 3px; }
  .legend { color: #888; font-size: 0.85rem; margin: 0.1rem 0 0.6rem; }
  th[title] { cursor: help; border-bottom: 1px dotted #888; }
</style>
</head>
<body>
<h1>Bridge Metrics</h1>
<p class="muted"><a href="{{.BasePath}}/">← Cameras</a> &nbsp;·&nbsp; <a href="{{.BasePath}}/metrics.prom">Prometheus</a> &nbsp;·&nbsp; <a href="{{.BasePath}}/api/metrics">JSON</a> &nbsp;·&nbsp; auto-refresh 10s</p>

{{- if .Issues}}
<h2 class="issue-err">Issues ({{len .Issues}})</h2>
<p class="legend">Soft failures the bridge wants you to know about. Each row stays until the underlying subsystem resolves it (auth recovers, camera reconnects, config is fixed). Hover any column header for details.</p>
<table>
  <thead><tr>
    <th title="error = a feature is degraded; warn = something's off but recoverable">Severity</th>
    <th title="Which subsystem reported the issue (auth, config, camera, record, mqtt, …)">Scope</th>
    <th title="Camera-specific issues name the camera; bridge-wide issues leave this blank">Camera</th>
    <th title="One-line summary + optional detail line beneath in muted text">Message</th>
    <th title="When the issue last fired (refreshed on every repeat report)">Last seen</th>
    <th title="How many times the issue has been reported since first sighting">Count</th>
  </tr></thead>
  <tbody>
  {{- range .Issues}}
    <tr>
      <td><span class="issue-{{if eq (print .Severity) "error"}}err{{else}}warn{{end}}">{{.Severity}}</span></td>
      <td>{{.Scope}}</td>
      <td>{{.Camera}}</td>
      <td>{{.Message}}{{if .Detail}}<br><span class="muted">{{.Detail}}</span>{{end}}</td>
      <td class="muted">{{sinceMs .LastSeen}} ago</td>
      <td>{{.Count}}</td>
    </tr>
  {{- end}}
  </tbody>
</table>
{{- end}}

<h2>Bridge</h2>
<p class="legend">Top-level state at a glance. Streaming + errored together equal the camera count when everything is reachable.</p>
<div class="summary">
  <div title="Cameras visible to the Wyze API after filtering"><b>{{.Bridge.CameraCount}}</b><span class="muted">cameras</span></div>
  <div title="Cameras currently in 'streaming' state (go2rtc has at least one producer)"><b>{{.Bridge.StreamingCount}}</b><span class="muted">streaming</span></div>
  <div title="Cameras stuck in 'error' state (waiting on backoff to retry)"><b>{{.Bridge.ErrorCount}}</b><span class="muted">errored</span></div>
  <div title="How long the bridge process has been running since last start"><b>{{uptime .Bridge.Uptime}}</b><span class="muted">uptime</span></div>
  <div title="Browser tabs currently subscribed to live SSE updates"><b>{{.Bridge.SSEClients}}</b><span class="muted">SSE clients</span></div>
  <div title="Bridge binary version"><b>{{.Bridge.Version}}</b><span class="muted">version</span></div>
</div>

<h2>Cameras</h2>
<p class="legend">One row per camera. Click the nickname to open its detail page.</p>
<table>
<thead><tr>
  <th title="Click to open the camera page. The lower line is the bridge-normalized name (URL-safe, used in MQTT topics and stream URLs)">Name</th>
  <th title="Marketing name + raw Wyze product code">Model</th>
  <th title="Streaming protocol the bridge uses for this camera: tutk = go2rtc direct, gwell = OG sidecar, webrtc = Wyze KVS via go2rtc">Path</th>
  <th title="streaming = live, connecting = handshake in progress, offline = idle, error = backed off after failures">State</th>
  <th title="hd or sd; override per-camera with QUALITY_&lt;CAM_NAME&gt;">Quality</th>
  <th title="Whether audio is muxed into the stream">Audio</th>
  <th title="Consecutive connect failures since last success; backoff doubles up to 5min">Errors</th>
  <th title="● = ffmpeg recorder running; the byte count is the current segment size">Recording</th>
</tr></thead>
<tbody>
{{- range .Cameras}}
  <tr>
    <td><a href="{{$.BasePath}}/camera/{{.Name}}">{{.Nickname}}</a><br><code class="muted">{{.Name}}</code></td>
    <td>{{.ModelName}}<br><code class="muted">{{.Model}}</code></td>
    <td>{{.Protocol}}{{if .ProtocolForced}} <span class="muted" title="Auto-promoted from TUTK after repeated failures — see /api/health for details">(forced)</span>{{end}}</td>
    <td><span class="pill state-{{.State}}">{{.State}}</span></td>
    <td>{{.Quality}}</td>
    <td>{{if .AudioOn}}✓{{else}}✗{{end}}</td>
    <td>{{.ErrorCount}}</td>
    <td>{{if .Recording}}● {{bytes .SessionBytes}}{{else}}—{{end}}</td>
  </tr>
{{- end}}
</tbody>
</table>

{{- if .WyzeAPI}}
<h2>Wyze Cloud API</h2>
<p class="legend">Per-endpoint call stats since the bridge started. A consistently-non-zero Errors column points at flaky network, throttling, or a broken endpoint.</p>
<table>
<thead><tr>
  <th title="API endpoint path on Wyze's cloud (api.wyzecam.com or app.wyzecam.com)">Endpoint</th>
  <th title="Total successful + failed calls">Calls</th>
  <th title="Calls that returned a non-OK Wyze code or HTTP error">Errors</th>
  <th title="Mean round-trip latency across all calls">Avg latency</th>
  <th title="HTTP status of the most recent call">Last status</th>
  <th title="How long ago the endpoint was last invoked">Last call</th>
</tr></thead>
<tbody>
{{- range .WyzeAPI}}
  <tr>
    <td><code>{{.Path}}</code></td>
    <td>{{.Count}}</td>
    <td>{{if gt .Errors 0}}<span class="issue-err">{{.Errors}}</span>{{else}}0{{end}}</td>
    <td>{{ms .AvgLatency}}</td>
    <td>{{.LastStatus}}</td>
    <td class="muted">{{sinceMs .LastCall}} ago</td>
  </tr>
{{- end}}
</tbody>
</table>
{{- end}}

{{- if .Storage}}
<h2>Storage</h2>
<p class="legend">Disk usage under RECORD_PATH, sampled every ~60s. Use this to size your media volume and verify the RECORD_KEEP pruner is doing its job.</p>
<p>Recordings total: <b>{{bytes .Storage.RecordingsTotalBytes}}</b>
   <span class="muted">(last refresh {{sinceMs .Storage.LastRefresh}} ago)</span></p>
{{- if .Storage.RecordingsPerCamera}}
<table>
<thead><tr><th>Camera</th><th>Bytes</th></tr></thead>
<tbody>
{{- range $cam, $bytes := .Storage.RecordingsPerCamera}}
  <tr><td><code>{{$cam}}</code></td><td>{{bytes $bytes}}</td></tr>
{{- end}}
</tbody>
</table>
{{- end}}
{{- end}}

{{- if .Events}}
<h2>Recent Events ({{len .Events}})</h2>
<p class="legend">Ring-buffered timeline of state changes, recording starts/stops, and other notable transitions. Not persisted across restarts.</p>
<details>
<summary>Show event log</summary>
<table>
<thead><tr><th>Time</th><th>Kind</th><th>Camera</th><th>Message</th></tr></thead>
<tbody>
{{- range .Events}}
  <tr>
    <td class="muted">{{.Time.Format "15:04:05"}}</td>
    <td>{{.Kind}}</td>
    <td>{{.Camera}}</td>
    <td>{{.Message}}</td>
  </tr>
{{- end}}
</tbody>
</table>
</details>
{{- end}}

</body>
</html>`
