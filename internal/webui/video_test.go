package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
)

func TestVideoLabel(t *testing.T) {
	// Known video states get a badge label; unknown or junk states get
	// none so the badge stays empty.
	for in, want := range map[string]string{"live": "live", "idle": "idle", "no_video": "no video", "unknown": "", "": "", "junk": ""} {
		if got := videoLabel(in); got != want {
			t.Errorf("videoLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatWhenAndError(t *testing.T) {
	// Times read as never, just now, minutes or hours ago, then a date.
	// Errors carry their age, and the badge title prefers the error
	// unless video is live, then falls back to the idle note.
	if got := formatWhen(time.Time{}); got != "never" {
		t.Errorf("zero time = %q", got)
	}
	if got := formatWhen(time.Now().Add(-10 * time.Second)); got != "just now" {
		t.Errorf("10s ago = %q", got)
	}
	if got := formatWhen(time.Now().Add(-5 * time.Minute)); got != "5m ago" {
		t.Errorf("5m ago = %q", got)
	}
	if got := formatWhen(time.Now().Add(-3 * time.Hour)); got != "3h ago" {
		t.Errorf("3h ago = %q", got)
	}
	if got := formatWhen(time.Now().Add(-72 * time.Hour)); !strings.Contains(got, "-") {
		t.Errorf("3 days ago should be a date, got %q", got)
	}
	if got := formatError(camera.Snapshot{}); got != "none" {
		t.Errorf("no error = %q", got)
	}
	snap := camera.Snapshot{LastError: "wyze: connect failed: discovery timeout", LastErrorAt: time.Now().Add(-2 * time.Minute)}
	if got := formatError(snap); got != "wyze: connect failed: discovery timeout (2m ago)" {
		t.Errorf("formatError = %q", got)
	}
	if got := videoTitle(snap); got != formatError(snap) {
		t.Errorf("title with error = %q", got)
	}
	if got := videoTitle(camera.Snapshot{Video: camera.VideoLive, LastVideo: time.Now(), LastError: "old news"}); got != "last video just now" {
		t.Errorf("live camera with an old error should show the video time, got %q", got)
	}
	if got := videoTitle(camera.Snapshot{LastVideo: time.Now()}); got != "last video just now" {
		t.Errorf("title with video = %q", got)
	}
	if got := videoTitle(camera.Snapshot{Video: camera.VideoIdle}); got != "nobody is pulling this stream" {
		t.Errorf("idle title = %q", got)
	}
	if got := videoTitle(camera.Snapshot{}); got != "" {
		t.Errorf("unknown title = %q", got)
	}
}

func TestRenderIndexAndDetail_VideoBadge(t *testing.T) {
	// Before the first poll the card has an empty unknown video badge, and
	// the detail page shows never for last video and none for last error.
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "kitchen")
	// Two polls with bytes: live.
	srv.camMgr.InjectCamera("kitchen", cam)

	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	// Not polled yet: the badge is present but empty (hidden by CSS).
	if !strings.Contains(body, `class="video-badge unknown" data-cam="kitchen"`) {
		t.Error("index should render an (empty) unknown video badge before the first poll")
	}

	w = httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/kitchen", nil))
	body = w.Body.String()
	for _, want := range []string{"Last video", "never", "Last error", "none"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	if w.Code != http.StatusOK {
		t.Errorf("detail status = %d", w.Code)
	}
}
