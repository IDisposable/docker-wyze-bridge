package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// The restyle keeps every hook app.js and the API tests depend on;
// this pins the new header pieces and the counts behind the chips.
func TestRenderIndex_HeaderChipsSearchTheme(t *testing.T) {
	srv, _ := testServer(t)
	for _, n := range []string{"kitchen", "garage", "porch"} {
		cam := injectStreamingCamera(srv, n)
		info := cam.GetInfo()
		info.Nickname = strings.ToUpper(n[:1]) + n[1:]
		cam.UpdateInfo(info)
	}
	srv.camMgr.SetIgnored([]wyzeapi.IgnoredCamera{{MAC: "AABB", Since: time.Now()}})

	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	for _, want := range []string{
		`class="brand"`,
		`data-chip="cameras"><b>3</b> cameras`,
		`data-chip="live"><b>0</b> live`,
		`data-chip="no_video"><b>0</b> no video`,
		`data-chip="ignored"><b>1</b> ignored`,
		`id="search"`,
		`id="theme-btn"`,
		`data-search="Kitchen V3 WYZE_CAKP2JFUS 10.0.0.1"`,
		`<details class="ignored" id="ignored">`,
		`Ignored cameras (1)`,
		`localStorage.getItem('wb-theme')`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
	// Chips with a zero count are dimmed; non-zero ones aren't.
	if !strings.Contains(body, `class="chip live zero"`) || !strings.Contains(body, `class="chip warn zero"`) {
		t.Error("zero-count chips should carry the zero class")
	}
	if strings.Contains(body, `chip zero" data-chip="ignored"`) {
		t.Error("ignored chip has a count and must not be dimmed")
	}
}

func TestRenderIndex_CardWithoutIPHasNoDanglingSeparator(t *testing.T) {
	// A camera with no LAN IP (a doorbell) ends its card's meta line at
	// the quality, with no trailing separator.
	srv, _ := testServer(t)
	bell := camera.NewCamera(wyzeapi.CameraInfo{Name: "front_door", Nickname: "Front door", Model: "GW_DBD", MAC: "GW_DBD_80482C0D6B01"}, "hd", true, false)
	bell.SetState(camera.StateStreaming)
	srv.camMgr.InjectCamera("front_door", bell)
	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `<p>Doorbell Duo &middot; hd</p>`) {
		t.Errorf("card meta line for an IP-less camera should end at the quality; got %s", snippet(body, "Doorbell Duo"))
	}
}

func TestRenderCamera_DetailHeader(t *testing.T) {
	// The detail page header has the back link, the camera's nickname,
	// its model version and the theme button.
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "kitchen")
	info := cam.GetInfo()
	info.Nickname = "Kitchen"
	cam.UpdateInfo(info)
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, httptest.NewRequest("GET", "/camera/kitchen", nil))
	body := w.Body.String()
	for _, want := range []string{`class="back"`, `<h1>Kitchen</h1>`, `<span class="version">V3</span>`, `id="theme-btn"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail header missing %q", want)
		}
	}
}

func snippet(body, around string) string {
	i := strings.Index(body, around)
	if i < 0 {
		return "(not found)"
	}
	end := i + 80
	if end > len(body) {
		end = len(body)
	}
	return body[i:end]
}
