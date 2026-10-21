package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestHandleAPISettings_RoundTripValidateAndApply(t *testing.T) {
	// Camera rules start empty (arrays, never null). A save is normalized,
	// persisted, logged and starts one discovery. Bad bodies are 400s
	// that change nothing, and an empty POST clears the rules.
	srv, _ := testServer(t)
	srv.SetMetricsSources(nil, nil, nil, NewEventLog(10))
	var mu sync.Mutex
	discovers := 0
	srv.OnDiscoverRequest(func(context.Context) { mu.Lock(); discovers++; mu.Unlock() })
	var persisted []wyzeapi.CameraRules
	srv.camMgr.OnRulesChange(func(r wyzeapi.CameraRules) { persisted = append(persisted, r) })

	// Defaults: any scope, empty arrays (never null), allow mode.
	w := httptest.NewRecorder()
	srv.handleAPISettings(w, httptest.NewRequest("GET", "/api/settings", nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"block":false,"macs":[],"models":[],"names":[],"scope":"any","subnets":[]}`+"\n" {
		t.Fatalf("GET default = %d %s", w.Code, w.Body.String())
	}

	// A save is normalized, persisted, echoed back and applied.
	w = httptest.NewRecorder()
	srv.handleAPISettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"scope":"Local","subnets":[" 192.168.20.5/24 "],"names":["Pet Cam*",""],"macs":["d0:3f:27:00:ca:04"],"models":["v4"],"block":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["scope"] != "local" || got["block"] != true {
		t.Errorf("echo = %v", got)
	}
	if s, _ := got["subnets"].([]interface{}); len(s) != 1 || s[0] != "192.168.20.0/24" {
		t.Errorf("subnets normalized = %v", got["subnets"])
	}
	if n, _ := got["names"].([]interface{}); len(n) != 1 || n[0] != "Pet Cam*" {
		t.Errorf("names = %v", got["names"])
	}
	if m, _ := got["macs"].([]interface{}); len(m) != 1 || m[0] != "D0:3F:27:00:CA:04" {
		t.Errorf("macs = %v", got["macs"])
	}
	if m, _ := got["models"].([]interface{}); len(m) != 1 || m[0] != "V4" {
		t.Errorf("models = %v", got["models"])
	}
	if len(persisted) != 1 || persisted[0].Scope != "local" {
		t.Errorf("persist hook got %+v", persisted)
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if discovers != 1 {
		t.Errorf("a save should kick one discovery, got %d", discovers)
	}
	mu.Unlock()
	if ev := srv.Events().Snapshot(); len(ev) != 1 || ev[0].Kind != "rules" || !strings.Contains(ev[0].Message, "local subnets only, block list of 3") {
		t.Errorf("event = %+v", ev)
	}

	// Bad bodies change nothing.
	huge := make([]string, maxRuleEntries+1)
	for i := range huge {
		huge[i] = "x"
	}
	hugeJSON, _ := json.Marshal(map[string]interface{}{"names": huge})
	for _, bad := range []string{`{"scope":"lan"}`, `{"subnets":["192.168.4.0"]}`, `{"subnets":["fd00::/64"]}`, `not json`, string(hugeJSON)} {
		w := httptest.NewRecorder()
		srv.handleAPISettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(bad)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%.40s: status %d, want 400", bad, w.Code)
		}
	}
	if r := srv.camMgr.Rules(); r.Scope != "local" || len(r.Names) != 1 || len(persisted) != 1 {
		t.Errorf("rejected bodies changed the rules: %+v (persisted %d)", r, len(persisted))
	}
	w = httptest.NewRecorder()
	srv.handleAPISettings(w, httptest.NewRequest("DELETE", "/api/settings", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d", w.Code)
	}

	// Clearing: POST is accepted as PUT, the summary says none.
	w = httptest.NewRecorder()
	srv.handleAPISettings(w, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{}`)))
	if w.Code != http.StatusOK || !srv.camMgr.Rules().Empty() {
		t.Errorf("clear = %d %s", w.Code, w.Body.String())
	}
	if ev := srv.Events().Snapshot(); !strings.HasSuffix(ev[0].Message, "saved: none") {
		t.Errorf("clear event = %+v", ev[0])
	}
}

func TestDescribeRules(t *testing.T) {
	// The one-line rules summary used in the event log reads right for
	// empty, scope only, allow and block list rules.
	for want, r := range map[string]wyzeapi.CameraRules{
		"none":                                {},
		"local subnets only":                  {Scope: "local"},
		"allow list of 2":                     {Names: []string{"a"}, MACs: []string{"b"}},
		"local subnets only, block list of 1": {Scope: "local", Models: []string{"V3"}, Block: true},
	} {
		if got := describeRules(r); got != want {
			t.Errorf("describeRules(%+v) = %q, want %q", r, got, want)
		}
	}
}

func TestSettingsPageAndExcludedStrip(t *testing.T) {
	// The settings page shows the saved rules, the local subnets and the
	// env filters. With nothing excluded the dashboard has no strip but
	// still links to settings, and /api/excluded is [].
	srv, _ := testServer(t)
	_, lan, _ := net.ParseCIDR("10.0.0.0/24")
	srv.camMgr.SetLocalSubnets(func() ([]*net.IPNet, error) { return []*net.IPNet{lan}, nil })
	srv.cfg.FilterNames, srv.cfg.FilterBlocks = []string{"OLD CAM"}, true
	if err := srv.camMgr.SetRules(wyzeapi.CameraRules{Scope: "local", Subnets: []string{"172.16.0.0/12"}, Names: []string{"Pet Cam*", "Mom's Porch"}, Block: true}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	srv.handleSettingsPage(w, httptest.NewRequest("GET", "/settings", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	for _, want := range []string{
		`<input type="radio" name="scope" value="local" checked>`,
		`<textarea name="subnets" rows="2" placeholder="192.168.20.0/24">172.16.0.0/12</textarea>`,
		"Pet Cam*\nMom&#39;s Porch</textarea>",
		`<input type="radio" name="block" value="true" checked>`,
		"<code>10.0.0.0/24, 172.16.0.0/12</code>",
		"FILTER_NAMES=OLD CAM; FILTER_BLOCKS=true",
		"Nothing. Every camera that passed the env filters is bridged.",
		`id="rules-form"`, `id="excluded-table"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	w = httptest.NewRecorder()
	srv.handleSettingsPage(w, httptest.NewRequest("GET", "/settings/extra", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("sub-path = %d, want 404", w.Code)
	}

	// Without excluded cameras the dashboard has no strip, but it does
	// link to the page. GET /api/excluded is [] not null.
	w = httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body = w.Body.String()
	if strings.Contains(body, `id="excluded"`) || !strings.Contains(body, `/settings" title="Camera rules`) {
		t.Error("dashboard strip/link wrong without exclusions")
	}
	w = httptest.NewRecorder()
	srv.handleAPIExcluded(w, httptest.NewRequest("GET", "/api/excluded", nil))
	if w.Body.String() != "[]\n" {
		t.Errorf("/api/excluded empty = %q", w.Body.String())
	}
}

// TestRenderIndex_ExcludedStrip runs a real discovery against a tiny
// Wyze mock with one off-LAN camera so the dashboard strip and the
// Settings table have something to show.
func TestRenderIndex_ExcludedStrip(t *testing.T) {
	wyzeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{"device_list": []interface{}{
				map[string]interface{}{"product_type": "Camera", "product_model": "WYZE_CAKP2JFUS", "mac": "AABB09", "nickname": "Mom's Porch", "enr": "e", "firmware_ver": "4.36.14",
					"device_params": map[string]interface{}{"p2p_id": "UID01234567890123458", "p2p_type": float64(4), "ip": "192.168.1.20", "dtls": float64(1), "camera_thumbnails": map[string]interface{}{}}},
				map[string]interface{}{"product_type": "Camera", "product_model": "WYZE_CAKP2JFUS", "mac": "AABB10", "nickname": "Side Garage", "enr": "e", "firmware_ver": "4.36.14",
					"device_params": map[string]interface{}{"p2p_id": "UID01234567890123459", "p2p_type": float64(4), "ip": "10.0.0.5", "dtls": float64(1), "camera_thumbnails": map[string]interface{}{}}},
			}},
		})
	}))
	t.Cleanup(wyzeSrv.Close)

	srv, _ := testServer(t)
	api := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	api.SetAuth(&wyzeapi.AuthState{AccessToken: "tok", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)})
	api.WyzeURL = wyzeSrv.URL
	srv.camMgr = camera.NewManager(srv.cfg, api, nil, zerolog.Nop())
	_, lan, _ := net.ParseCIDR("10.0.0.0/24")
	srv.camMgr.SetLocalSubnets(func() ([]*net.IPNet, error) { return []*net.IPNet{lan}, nil })
	if err := srv.camMgr.SetRules(wyzeapi.CameraRules{Scope: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.camMgr.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	for _, want := range []string{
		`<details class="ignored excluded" id="excluded">`,
		"Excluded by rules (1)",
		`<tr data-mac="AABB09">`, "Mom&#39;s Porch", "<code>192.168.1.20</code>", "off-LAN: 192.168.1.20 isn&#39;t in 10.0.0.0/24",
		`data-cam="side_garage"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, `data-cam="mom`) {
		t.Error("excluded camera must not get a card")
	}

	w = httptest.NewRecorder()
	srv.handleAPIExcluded(w, httptest.NewRequest("GET", "/api/excluded", nil))
	var list []camera.Excluded
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].MAC != "AABB09" || list[0].Nickname != "Mom's Porch" || !strings.HasPrefix(list[0].Reason, "off-LAN") {
		t.Errorf("/api/excluded = %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handleSettingsPage(w, httptest.NewRequest("GET", "/settings", nil))
	if body := w.Body.String(); !strings.Contains(body, "<td>Mom&#39;s Porch</td>") || strings.Contains(body, "Nothing. Every camera") {
		t.Error("settings page should list the excluded camera")
	}
}

func TestSettingsPage_QualityTable(t *testing.T) {
	// One select per TUTK camera, preset to the saved choice; a camera
	// that picks its own quality (a doorbell) gets a note instead; a
	// change POSTs to the camera's quality action and "" means default.
	srv, _ := testServer(t)
	injectStreamingCamera(srv, "garage")
	door := camera.NewCamera(wyzeapi.CameraInfo{Name: "front_door", Model: "GW_DBD", MAC: "AABBCCDDEEFF", Nickname: "Front door"}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", door)

	post := func(name, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/"+name+"/quality", strings.NewReader(body)))
		return w
	}
	if w := post("garage", `{"quality":"sd"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"quality":"sd"`) {
		t.Fatalf("sd: %d %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	srv.handleSettingsPage(w, httptest.NewRequest("GET", "/settings", nil))
	body := w.Body.String()
	if !strings.Contains(body, `data-cam="garage"`) || !strings.Contains(body, `<option value="sd" selected>SD</option>`) {
		t.Errorf("garage's select should show the saved SD:\n%s", between(body, `id="quality-table"`, "</table>"))
	}
	if !strings.Contains(body, `<option value="">Default (hd)</option>`) {
		t.Error("the default option should name the env default")
	}
	if strings.Contains(body, `data-cam="front_door"`) || !strings.Contains(body, "set by the camera") {
		t.Error("the doorbell should get a note, not a select")
	}

	if w := post("garage", `{"quality":""}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"quality":"hd"`) {
		t.Errorf("back to default: %d %s", w.Code, w.Body.String())
	}
	if w := post("front_door", `{"quality":"sd"}`); w.Code != http.StatusBadRequest {
		t.Errorf("doorbell quality: %d, want 400", w.Code)
	}
}
