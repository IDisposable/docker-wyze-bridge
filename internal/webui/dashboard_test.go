package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleDashboardYAML_Smoke renders the auto-generated Lovelace
// YAML and checks the structural skeleton: title, summary glance,
// markdown card. With no cameras registered there are still bridge
// sensors to render.
func TestHandleDashboardYAML_Smoke(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/dashboard.yaml", nil)
	w := httptest.NewRecorder()
	srv.handleDashboardYAML(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	mustHave := []string{
		"title: Wyze Bridge",
		"type: glance",
		"sensor.bridge_cameras",
		"sensor.bridge_streaming",
		"sensor.bridge_recordings_size",
		"type: markdown",
	}
	for _, want := range mustHave {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in dashboard yaml\n--- body ---\n%s", want, body)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("content-type = %q, want application/yaml", ct)
	}
}

// TestYAMLQuote covers the few characters that need explicit
// double-quoting in a YAML scalar (the rest pass through unchanged).
func TestYAMLQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"has space", "has space"},     // space alone isn't a special char
		{"colon: here", `"colon: here"`},
		{`with"quote`, `"with\"quote"`},
		{"", `""`},
	}
	for _, c := range cases {
		got := yamlQuote(c.in)
		if got != c.want {
			t.Errorf("yamlQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
