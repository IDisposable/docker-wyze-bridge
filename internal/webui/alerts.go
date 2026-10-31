package webui

import (
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/alerts"
)

// SetAlerts attaches the Wyze event mirror.
func (s *Server) SetAlerts(m *alerts.Manager) { s.alerts = m }

// BroadcastAlert pushes a new alert to open browser tabs over SSE.
func (s *Server) BroadcastAlert(a alerts.Alert) { s.sseHub.SendJSON("alert", a) }

// GET /api/alerts?camera=<name>&limit=N
func (s *Server) handleAPIAlerts(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "alerts not enabled")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	writeJSON(w, map[string]interface{}{"alerts": s.alerts.List(r.URL.Query().Get("camera"), limit)})
}

// GET /alerts/media/<camera>/<date>/<file> — serves stored snapshots/clips.
func (s *Server) handleAlertMedia(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/alerts/media/")
	clean := filepath.Clean("/" + rel)[1:]
	if clean == "" || strings.Contains(clean, "..") || clean == "index.json" {
		http.NotFound(w, r)
		return
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".jpg", ".mp4":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, filepath.Join(s.alerts.Dir(), clean))
}

type alertView struct {
	alerts.Alert
	When     string
	Day      string
	ImageURL string
	ClipURL  string
}

// GET /alerts?camera=<name>
func (s *Server) handleAlertsPage(w http.ResponseWriter, r *http.Request) {
	base := ingressBasePath(r)
	cam := r.URL.Query().Get("camera")
	var views []alertView
	if s.alerts != nil {
		for _, a := range s.alerts.List(cam, 300) {
			t := time.UnixMilli(a.Time)
			v := alertView{Alert: a, When: t.Format("3:04:05 PM"), Day: dayLabel(t)}
			if a.Image != "" {
				v.ImageURL = base + "/alerts/media/" + filepath.ToSlash(a.Image)
			}
			if a.Clip != "" {
				v.ClipURL = base + "/alerts/media/" + filepath.ToSlash(a.Clip)
			}
			views = append(views, v)
		}
	}
	type camOpt struct{ Name, Nickname string }
	var cams []camOpt
	for _, c := range s.camMgr.Cameras() {
		cams = append(cams, camOpt{c.Name(), c.GetInfo().Nickname})
	}
	tmpl, err := template.New("alerts").Parse(alertsHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, map[string]interface{}{
		"Version": s.version, "BasePath": base, "Alerts": views, "Cameras": cams,
		"Camera": cam, "Enabled": s.alerts != nil && s.alerts.Enabled(),
	})
}

func dayLabel(t time.Time) string {
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "Today"
	case now.AddDate(0, 0, -1).Format("2006-01-02") == t.Format("2006-01-02"):
		return "Yesterday"
	default:
		return t.Format("Mon, Jan 2")
	}
}

const alertsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Alerts - Wyze Bridge</title>
    <link rel="icon" type="image/png" href="{{.BasePath}}/favicon.ico">
    <link rel="stylesheet" href="{{.BasePath}}/static/style.css">
    <script>window.__BASE_PATH = '{{.BasePath}}';</script>
</head>
<body>
    <header>
        <a href="{{.BasePath}}/" class="back">&larr; All Cameras</a>
        <h1>Alerts <span class="version">v{{.Version}}</span></h1>
        <nav class="header-nav">
            <a href="{{.BasePath}}/metrics" title="Metrics, issues, events">Metrics</a>
        </nav>
    </header>
    <main class="alerts-page">
        <nav class="alert-filter">
            <a href="{{.BasePath}}/alerts" class="{{if not .Camera}}on{{end}}">All</a>
            {{range .Cameras}}<a href="{{$.BasePath}}/alerts?camera={{.Name}}" class="{{if eq $.Camera .Name}}on{{end}}">{{.Nickname}}</a>{{end}}
        </nav>
        {{if not .Enabled}}<p class="alert-empty">Alerts are disabled (ALERTS_INTERVAL=0).</p>{{end}}
        {{$day := ""}}
        {{range .Alerts}}
            {{if ne .Day $day}}{{$day = .Day}}<h2 class="alert-day">{{.Day}}</h2>{{end}}
            <article class="alert-row" data-kind="{{.Kind}}">
                {{if .ImageURL}}<a href="{{if .ClipURL}}{{.ClipURL}}{{else}}{{.ImageURL}}{{end}}" target="_blank" rel="noopener" class="alert-thumb">
                    <img src="{{.ImageURL}}" alt="{{.Kind}} at {{.When}}" loading="lazy">
                    {{if .ClipURL}}<span class="alert-play" title="Play clip">&#9654;</span>{{end}}
                </a>{{else}}<div class="alert-thumb alert-nothumb">no snapshot</div>{{end}}
                <div class="alert-info">
                    <strong>{{if eq .Kind "motion"}}Motion detected{{else if eq .Kind "sound"}}Sound detected{{else}}{{.Kind}}{{end}}</strong>
                    {{range .Tags}}<span class="alert-tag">{{.}}</span>{{end}}
                    <div class="alert-meta">{{.Nickname}} &middot; {{.When}}</div>
                </div>
            </article>
        {{else}}
            {{if .Enabled}}<p class="alert-empty">No alerts yet. New events appear within a minute of Wyze recording them.</p>{{end}}
        {{end}}
    </main>
    <script>
    (function() {
        var es = new EventSource((window.__BASE_PATH || '') + '/events');
        es.addEventListener('alert', function() { location.reload(); });
    })();
    </script>
</body>
</html>`
