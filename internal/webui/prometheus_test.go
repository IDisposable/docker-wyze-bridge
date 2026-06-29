package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlePrometheus_BasicMetricsPresent renders /metrics.prom and
// checks every expected metric family is emitted with the proper
// HELP + TYPE preambles and a numeric line.
func TestHandlePrometheus_BasicMetricsPresent(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/metrics.prom", nil)
	w := httptest.NewRecorder()
	srv.handlePrometheus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain*", ct)
	}

	body := w.Body.String()
	mustHave := []string{
		"# HELP wyze_bridge_uptime_seconds",
		"# TYPE wyze_bridge_uptime_seconds gauge",
		"wyze_bridge_uptime_seconds ",
		"# HELP wyze_bridge_cameras_total",
		"# TYPE wyze_bridge_cameras_total gauge",
		"wyze_bridge_cameras_total ",
	}
	for _, want := range mustHave {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in prometheus output\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandlePrometheus_LabelEscaping makes sure label values from
// camera names with special characters don't break the line format.
// The fmtLabels helper should quote/escape any quote characters.
func TestHandlePrometheus_LabelEscaping(t *testing.T) {
	labels := map[string]string{
		"camera": `front"door`,
		"state":  "streaming",
	}
	got := fmtLabels(labels)
	if !strings.Contains(got, `\"`) {
		t.Errorf("fmtLabels did not escape embedded quote: %q", got)
	}
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("fmtLabels not wrapped in braces: %q", got)
	}
}
