package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleMetricsJSON_Shape checks the top-level snapshot keys
// every consumer (the metrics page itself, external scrapers) relies
// on. Empty cameras list is still a valid shape; we want the bridge
// summary fields present.
func TestHandleMetricsJSON_Shape(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetricsJSON(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var snap map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}

	bridge, ok := snap["bridge"].(map[string]interface{})
	if !ok {
		t.Fatal("missing bridge object")
	}
	for _, k := range []string{"version", "uptime_s", "started_at", "camera_count"} {
		if _, ok := bridge[k]; !ok {
			t.Errorf("bridge missing %q", k)
		}
	}
	if _, ok := snap["cameras"]; !ok {
		t.Error("missing cameras key")
	}
}

// TestHandleMetricsPage_RendersHTML smokes the HTML page so a broken
// template (mismatched range / undefined field) fails the test, not
// production.
func TestHandleMetricsPage_RendersHTML(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetricsPage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<h1>Bridge Metrics</h1>", "<h2>Bridge</h2>", "<h2>Cameras</h2>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in HTML output", want)
		}
	}
}
