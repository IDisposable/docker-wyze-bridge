package webui

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// handlePrometheus emits the bridge's metrics in Prometheus text
// exposition format (OpenMetrics-compatible). No external dependency
// — the format is simple enough to hand-write, and we avoid pulling
// in the prometheus/client_golang package for a handful of gauges.
//
// Naming convention: all metrics are prefixed `wyze_bridge_`.
//
//	wyze_bridge_uptime_seconds
//	wyze_bridge_cameras_total
//	wyze_bridge_cameras_streaming
//	wyze_bridge_cameras_errored
//	wyze_bridge_camera_error_count{camera=,model=,state=}
//	wyze_bridge_camera_recording{camera=}
//	wyze_bridge_camera_recording_bytes{camera=}
//	wyze_bridge_issues_total
//	wyze_bridge_wyzeapi_calls_total{endpoint=}
//	wyze_bridge_wyzeapi_errors_total{endpoint=}
//	wyze_bridge_wyzeapi_latency_seconds{endpoint=}
//	wyze_bridge_recordings_bytes_total
//	wyze_bridge_recordings_bytes{camera=}
//	wyze_bridge_sse_clients
func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	snap := s.buildMetricsSnapshot()

	writeGauge(w, "wyze_bridge_uptime_seconds",
		"Bridge process uptime in seconds.",
		float64(snap.Bridge.Uptime), nil)

	writeGauge(w, "wyze_bridge_cameras_total",
		"Total cameras discovered.",
		float64(snap.Bridge.CameraCount), nil)

	writeGauge(w, "wyze_bridge_cameras_streaming",
		"Cameras currently in streaming state.",
		float64(snap.Bridge.StreamingCount), nil)

	writeGauge(w, "wyze_bridge_cameras_errored",
		"Cameras currently in error state.",
		float64(snap.Bridge.ErrorCount), nil)

	writeGauge(w, "wyze_bridge_sse_clients",
		"Active SSE client connections.",
		float64(snap.Bridge.SSEClients), nil)

	if snap.Issues != nil {
		writeGauge(w, "wyze_bridge_issues_total",
			"Active entries in the issues registry.",
			float64(len(snap.Issues)), nil)
	}

	// Per-camera
	fmt.Fprintln(w, "# HELP wyze_bridge_camera_error_count Error counter per camera.")
	fmt.Fprintln(w, "# TYPE wyze_bridge_camera_error_count gauge")
	fmt.Fprintln(w, "# HELP wyze_bridge_camera_recording Whether the camera is currently being recorded (1/0).")
	fmt.Fprintln(w, "# TYPE wyze_bridge_camera_recording gauge")
	fmt.Fprintln(w, "# HELP wyze_bridge_camera_recording_bytes Bytes written to the current recording segment.")
	fmt.Fprintln(w, "# TYPE wyze_bridge_camera_recording_bytes gauge")
	for _, c := range snap.Cameras {
		labels := map[string]string{
			"camera":   c.Name,
			"model":    c.Model,
			"protocol": c.Protocol,
			"state":    c.State,
		}
		fmt.Fprintf(w, "wyze_bridge_camera_error_count%s %d\n", fmtLabels(labels), c.ErrorCount)
		recording := 0
		if c.Recording {
			recording = 1
		}
		fmt.Fprintf(w, "wyze_bridge_camera_recording%s %d\n", fmtLabels(map[string]string{"camera": c.Name}), recording)
		if c.Recording {
			fmt.Fprintf(w, "wyze_bridge_camera_recording_bytes%s %d\n", fmtLabels(map[string]string{"camera": c.Name}), c.SessionBytes)
		}
	}

	// Wyze API
	if len(snap.WyzeAPI) > 0 {
		fmt.Fprintln(w, "# HELP wyze_bridge_wyzeapi_calls_total Wyze cloud API calls by endpoint.")
		fmt.Fprintln(w, "# TYPE wyze_bridge_wyzeapi_calls_total counter")
		fmt.Fprintln(w, "# HELP wyze_bridge_wyzeapi_errors_total Wyze cloud API errors by endpoint.")
		fmt.Fprintln(w, "# TYPE wyze_bridge_wyzeapi_errors_total counter")
		fmt.Fprintln(w, "# HELP wyze_bridge_wyzeapi_latency_seconds Latest Wyze cloud API call latency by endpoint.")
		fmt.Fprintln(w, "# TYPE wyze_bridge_wyzeapi_latency_seconds gauge")
		for _, e := range snap.WyzeAPI {
			labels := map[string]string{"endpoint": e.Path}
			fmt.Fprintf(w, "wyze_bridge_wyzeapi_calls_total%s %d\n", fmtLabels(labels), e.Count)
			fmt.Fprintf(w, "wyze_bridge_wyzeapi_errors_total%s %d\n", fmtLabels(labels), e.Errors)
			fmt.Fprintf(w, "wyze_bridge_wyzeapi_latency_seconds%s %f\n", fmtLabels(labels), e.LastLatency.Seconds())
		}
	}

	// Storage
	if snap.Storage != nil {
		writeGauge(w, "wyze_bridge_recordings_bytes_total",
			"Total bytes across all recording segments.",
			float64(snap.Storage.RecordingsTotalBytes), nil)
		fmt.Fprintln(w, "# HELP wyze_bridge_recordings_bytes Bytes of recordings per camera.")
		fmt.Fprintln(w, "# TYPE wyze_bridge_recordings_bytes gauge")
		for cam, bytes := range snap.Storage.RecordingsPerCamera {
			fmt.Fprintf(w, "wyze_bridge_recordings_bytes%s %d\n", fmtLabels(map[string]string{"camera": cam}), bytes)
		}
	}
}

func writeGauge(w io.Writer, name, help string, value float64, labels map[string]string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	fmt.Fprintf(w, "%s%s %g\n", name, fmtLabels(labels), value)
}

// fmtLabels emits `{k="v",k2="v2"}` or empty string when labels is nil.
// Values are escaped per Prometheus exposition rules (backslash, quote,
// newline). Labels with an empty value are omitted entirely.
func fmtLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	// Stable key ordering
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sortStrings(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		for _, r := range labels[k] {
			switch r {
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			case '\n':
				b.WriteString(`\n`)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

