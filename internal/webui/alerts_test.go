package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/alerts"
	"github.com/rs/zerolog"
)

type noEvents struct{}

func (noEvents) GetEventList([]string, int64, int64) ([]map[string]interface{}, error) {
	return nil, nil
}

func TestAlertsPageAndMedia(t *testing.T) {
	srv, _ := testServer(t)
	dir := t.TempDir()
	day := filepath.Join(dir, "porch", "2026-10-11")
	os.MkdirAll(day, 0o755)
	os.WriteFile(filepath.Join(day, "171000_motion.jpg"), []byte{0xff, 0xd8}, 0o644)
	idx := `[{"id":"e1","camera":"porch","nickname":"Front Porch","ts":` +
		strconv.FormatInt(time.Now().UnixMilli(), 10) + `,"kind":"motion","tags":["person"],"image":"porch/2026-10-11/171000_motion.jpg"}]`
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644)
	srv.SetAlerts(alerts.NewManager(dir, time.Minute, 0, noEvents{}, func() []alerts.Camera { return nil }, zerolog.Nop()))

	w := httptest.NewRecorder()
	srv.handleAlertsPage(w, httptest.NewRequest("GET", "/alerts", nil))
	body := w.Body.String()
	for _, want := range []string{"Motion detected", "Front Porch", "person", "/alerts/media/porch/2026-10-11/171000_motion.jpg", "Today"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	w = httptest.NewRecorder()
	srv.handleAlertMedia(w, httptest.NewRequest("GET", "/alerts/media/porch/2026-10-11/171000_motion.jpg", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 2 {
		t.Errorf("media: %d len=%d", w.Code, w.Body.Len())
	}
	for _, bad := range []string{"/alerts/media/index.json", "/alerts/media/../../etc/passwd", "/alerts/media/porch/x.txt"} {
		w = httptest.NewRecorder()
		srv.handleAlertMedia(w, httptest.NewRequest("GET", bad, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: code %d, want 404", bad, w.Code)
		}
	}

	w = httptest.NewRecorder()
	srv.handleAPIAlerts(w, httptest.NewRequest("GET", "/api/alerts?camera=porch", nil))
	if !strings.Contains(w.Body.String(), `"kind":"motion"`) {
		t.Errorf("api: %s", w.Body)
	}
}
