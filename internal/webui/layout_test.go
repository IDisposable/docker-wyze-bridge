package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func cams(names ...string) []*camera.Camera {
	out := make([]*camera.Camera, 0, len(names))
	for _, n := range names {
		out = append(out, camera.NewCamera(wyzeapi.CameraInfo{Name: n, Nickname: n}, "hd", true, false))
	}
	return out
}

func names(cs []*camera.Camera) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.Name()
	}
	return strings.Join(parts, ",")
}

func TestOrderCameras(t *testing.T) {
	// Saved order comes first, cameras it doesn't mention follow by name,
	// and unknown or repeated names in the order are ignored.
	all := cams("zebra", "apple", "mango", "banana")
	cases := []struct {
		name  string
		order []string
		want  string
	}{
		{"no layout: by name", nil, "apple,banana,mango,zebra"},
		{"full order kept", []string{"mango", "zebra", "banana", "apple"}, "mango,zebra,banana,apple"},
		{"partial order, rest by name", []string{"zebra"}, "zebra,apple,banana,mango"},
		{"unknown names skipped", []string{"ghost", "mango", "ghost2"}, "mango,apple,banana,zebra"},
		{"duplicates in order placed once", []string{"mango", "mango", "apple"}, "mango,apple,banana,zebra"},
	}
	for _, c := range cases {
		if got := names(orderCameras(all, c.order)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if got := orderCameras(nil, []string{"a"}); len(got) != 0 {
		t.Errorf("no cameras: %v", got)
	}
}

func TestHandleAPILayout_GetDefault(t *testing.T) {
	// With nothing saved the layout is unlocked and empty, and the order
	// encodes as [] rather than null for the JS side.
	srv, _ := testServer(t)
	w := httptest.NewRecorder()
	srv.handleAPILayout(w, httptest.NewRequest("GET", "/api/layout", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	raw := w.Body.String()
	var got wyzeapi.Layout
	_ = json.Unmarshal([]byte(raw), &got)
	if got.Locked || len(got.Order) != 0 {
		t.Errorf("default layout = %+v, want unlocked and empty", got)
	}
	if !strings.Contains(raw, `"order":[]`) {
		t.Errorf("empty order should encode as [] not null: %s", raw)
	}
}

func TestHandleAPILayout_PutRoundTripAndPersist(t *testing.T) {
	// A saved layout drops duplicates and blanks, is handed to the persist
	// hook as its own copy (no shared slice), and POST works like PUT for
	// proxies that strip PUT.
	srv, _ := testServer(t)
	var saved []wyzeapi.Layout
	srv.OnLayoutChange(func(l wyzeapi.Layout) { saved = append(saved, l) })

	body := bytes.NewBufferString(`{"order":["side_garage","driveway","side_garage","","pet_cam"],"locked":true}`)
	w := httptest.NewRecorder()
	srv.handleAPILayout(w, httptest.NewRequest("PUT", "/api/layout", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got := srv.Layout()
	if !got.Locked || strings.Join(got.Order, ",") != "side_garage,driveway,pet_cam" {
		t.Errorf("stored layout = %+v (duplicates and blanks should be dropped)", got)
	}
	if len(saved) != 1 || !saved[0].Locked || len(saved[0].Order) != 3 {
		t.Errorf("persist hook got %+v", saved)
	}
	// The hook's copy and the store are independent of the caller's slice.
	saved[0].Order[0] = "mutated"
	if srv.Layout().Order[0] != "side_garage" {
		t.Error("layout store aliased the persisted slice")
	}
	// POST is accepted as an alias for PUT (proxies that strip PUT).
	w = httptest.NewRecorder()
	srv.handleAPILayout(w, httptest.NewRequest("POST", "/api/layout", bytes.NewBufferString(`{"order":["a"],"locked":false}`)))
	if w.Code != http.StatusOK || srv.Layout().Locked {
		t.Errorf("POST: %d locked=%v", w.Code, srv.Layout().Locked)
	}
}

func TestHandleAPILayout_Rejects(t *testing.T) {
	// Bad JSON, wrong types, too many entries and other methods are
	// refused, and none of them change the stored layout.
	srv, _ := testServer(t)
	huge := make([]string, maxLayoutEntries+1)
	for i := range huge {
		huge[i] = "cam" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
	}
	hugeJSON, _ := json.Marshal(wyzeapi.Layout{Order: huge})
	cases := []struct {
		method, body string
		want         int
	}{
		{"PUT", `not json`, http.StatusBadRequest},
		{"PUT", `{"order":"side_garage"}`, http.StatusBadRequest}, // string, not array
		{"PUT", `{"order":[1,2]}`, http.StatusBadRequest},         // numbers, not names
		{"PUT", string(hugeJSON), http.StatusBadRequest},
		{"DELETE", ``, http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		srv.handleAPILayout(w, httptest.NewRequest(c.method, "/api/layout", bytes.NewBufferString(c.body)))
		if w.Code != c.want {
			t.Errorf("%s %q = %d, want %d", c.method, c.body[:min(len(c.body), 30)], w.Code, c.want)
		}
	}
	if l := srv.Layout(); len(l.Order) != 0 {
		t.Errorf("rejected bodies must not change the layout: %+v", l)
	}
}

func TestRenderIndex_UsesLayoutOrderAndLock(t *testing.T) {
	// The dashboard draws cards in the saved order and shows the locked
	// grid and lock button when locked. Without a layout it falls back to
	// an arranging grid in name order.
	srv, _ := testServer(t)
	for _, n := range []string{"zebra", "apple", "mango"} {
		injectStreamingCamera(srv, n)
	}
	srv.SetLayout(wyzeapi.Layout{Order: []string{"mango", "zebra"}, Locked: true})

	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	iMango, iZebra, iApple := strings.Index(body, `data-cam="mango"`), strings.Index(body, `data-cam="zebra"`), strings.Index(body, `data-cam="apple"`)
	if !(iMango < iZebra && iZebra < iApple) {
		t.Errorf("cards not in layout order: mango@%d zebra@%d apple@%d", iMango, iZebra, iApple)
	}
	if !strings.Contains(body, `class="camera-grid locked"`) || !strings.Contains(body, `data-locked="true"`) || !strings.Contains(body, "🔒 Locked") {
		t.Error("locked layout should render a locked grid and lock button")
	}
	if !strings.Contains(body, `class="drag-handle"`) {
		t.Error("cards should carry a drag handle (hidden by CSS when locked)")
	}

	srv.SetLayout(wyzeapi.Layout{})
	w = httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	body = w.Body.String()
	if !strings.Contains(body, `class="camera-grid arranging"`) || !strings.Contains(body, "🔓 Arrange") {
		t.Error("default layout should render an arranging grid")
	}
	iApple, iMango = strings.Index(body, `data-cam="apple"`), strings.Index(body, `data-cam="mango"`)
	if iApple > iMango {
		t.Error("without a layout, cards should be in name order")
	}
}
