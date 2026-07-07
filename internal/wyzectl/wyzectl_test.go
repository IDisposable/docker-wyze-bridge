package wyzectl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// wyzectl uses go2rtc's fork HTTP endpoints; every method must:
// (1) hit the right URL with `src=<cam>`, (2) send the right JSON body,
// (3) forward Basic auth when configured, (4) map 4xx/5xx into the
// typed errors.

type gotReq struct {
	method string
	path   string
	src    string
	body   string
	auth   string
}

func newRecorder(t *testing.T, status int, respBody string) (*httptest.Server, *[]gotReq) {
	t.Helper()
	var got []gotReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, gotReq{
			method: r.Method,
			path:   r.URL.Path,
			src:    r.URL.Query().Get("src"),
			body:   string(body),
			auth:   r.Header.Get("Authorization"),
		})
		w.WriteHeader(status)
		if respBody != "" {
			_, _ = w.Write([]byte(respBody))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func newClient(base string) *Client {
	return NewClient(base, zerolog.Nop())
}

// ── Method-per-endpoint routing ─────────────────────────────────────

func TestClient_MethodRouting(t *testing.T) {
	cases := []struct {
		name     string
		call     func(*Client) error
		wantPath string
		wantBody string
	}{
		{"spotlight on", func(c *Client) error { return c.SetSpotlight(context.Background(), "cam", true) },
			"/api/wyze/spotlight", `{"on":true}`},
		{"spotlight off", func(c *Client) error { return c.SetSpotlight(context.Background(), "cam", false) },
			"/api/wyze/spotlight", `{"on":false}`},
		{"ir_led on", func(c *Client) error { return c.SetIRLED(context.Background(), "cam", true) },
			"/api/wyze/ir_led", `{"mode":1}`}, // 850nm long-range
		{"ir_led off", func(c *Client) error { return c.SetIRLED(context.Background(), "cam", false) },
			"/api/wyze/ir_led", `{"mode":2}`}, // 940nm short-range
		{"night_vision on", func(c *Client) error { return c.SetNightVision(context.Background(), "cam", NightVisionOn) },
			"/api/wyze/night_vision", `{"mode":1}`},
		{"night_vision off", func(c *Client) error { return c.SetNightVision(context.Background(), "cam", NightVisionOff) },
			"/api/wyze/night_vision", `{"mode":2}`},
		{"night_vision auto", func(c *Client) error { return c.SetNightVision(context.Background(), "cam", NightVisionAuto) },
			"/api/wyze/night_vision", `{"mode":3}`},
		{"siren on", func(c *Client) error { return c.SetSiren(context.Background(), "cam", true) },
			"/api/wyze/siren", `{"on":true}`},
		{"hor_flip", func(c *Client) error { return c.SetHorizontalFlip(context.Background(), "cam", true) },
			"/api/wyze/hor_flip", `{"on":true}`},
		{"ver_flip", func(c *Client) error { return c.SetVerticalFlip(context.Background(), "cam", true) },
			"/api/wyze/ver_flip", `{"on":true}`},
		{"take_photo", func(c *Client) error { return c.TakePhoto(context.Background(), "cam") },
			"/api/wyze/take_photo", ""},
		{"rotate_action left @ 50",
			func(c *Client) error { return c.RotateByAction(context.Background(), "cam", RotationLeft, 50) },
			"/api/wyze/rotate_action", `{"direction":1,"speed":50}`},
		{"reset_rotation",
			func(c *Client) error { return c.ResetRotation(context.Background(), "cam") },
			"/api/wyze/reset_rotation", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := newRecorder(t, http.StatusOK, "")
			c := newClient(srv.URL)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 request, got %d", len(*got))
			}
			r := (*got)[0]
			if r.method != "POST" {
				t.Errorf("method = %q, want POST", r.method)
			}
			if r.path != tc.wantPath {
				t.Errorf("path = %q, want %q", r.path, tc.wantPath)
			}
			if r.src != "cam" {
				t.Errorf("src = %q, want cam", r.src)
			}
			if r.body != tc.wantBody {
				t.Errorf("body = %q, want %q", r.body, tc.wantBody)
			}
		})
	}
}

func TestClient_RotateByDegree_EncodesInt16(t *testing.T) {
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	if err := c.RotateByDegree(context.Background(), "cam", -30, 15, 60); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d", len(*got))
	}
	// JSON marshals int16 as numbers, no wire-format encoding at this layer;
	// the fork's Go handler receives them as int16 via json.Unmarshal.
	var body map[string]int
	if err := json.Unmarshal([]byte((*got)[0].body), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["h"] != -30 || body["v"] != 15 || body["speed"] != 60 {
		t.Errorf("body = %+v", body)
	}
}

func TestClient_RotateByDegree_RejectsFirmwareVLimit(t *testing.T) {
	// Firmware issue #862: vertical byte-range limit is int8 (-128..127)
	// even though the wire format nominally allocates int16. Reject
	// client-side.
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	if err := c.RotateByDegree(context.Background(), "cam", 0, 200, 50); err == nil {
		t.Fatal("expected out-of-range error for vDeg=200")
	}
	if err := c.RotateByDegree(context.Background(), "cam", 0, -200, 50); err == nil {
		t.Fatal("expected out-of-range error for vDeg=-200")
	}
	if len(*got) != 0 {
		t.Errorf("expected zero requests to be sent, got %d", len(*got))
	}
}

func TestClient_SetPTZPosition_ClampsRange(t *testing.T) {
	// K11018 payload uses uint8 vertical (0-40) and uint16 horizontal
	// (0-350). Enforce client-side.
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	if err := c.SetPTZPosition(context.Background(), "cam", 41, 0); err == nil {
		t.Fatal("expected error for vertical=41")
	}
	if err := c.SetPTZPosition(context.Background(), "cam", 0, 351); err == nil {
		t.Fatal("expected error for horizontal=351")
	}
	if len(*got) != 0 {
		t.Errorf("expected zero requests to be sent, got %d", len(*got))
	}

	if err := c.SetPTZPosition(context.Background(), "cam", 20, 175); err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*got))
	}
	// Verify {"h":175,"v":20} — timestamp is injected by the fork's HTTP
	// handler, not by us.
	if !strings.Contains((*got)[0].body, `"v":20`) || !strings.Contains((*got)[0].body, `"h":175`) {
		t.Errorf("body = %q", (*got)[0].body)
	}
}

// ── Basic auth ──────────────────────────────────────────────────────

func TestClient_BasicAuth_AttachedWhenConfigured(t *testing.T) {
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	c.SetBasicAuth("admin", "hunter2")

	_ = c.SetSpotlight(context.Background(), "cam", true)

	if len(*got) != 1 {
		t.Fatalf("requests = %d", len(*got))
	}
	// "admin:hunter2" → "YWRtaW46aHVudGVyMg=="
	want := "Basic YWRtaW46aHVudGVyMg=="
	if (*got)[0].auth != want {
		t.Errorf("Authorization = %q, want %q", (*got)[0].auth, want)
	}
}

func TestClient_BasicAuth_OmittedByDefault(t *testing.T) {
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	_ = c.SetSpotlight(context.Background(), "cam", true)

	if (*got)[0].auth != "" {
		t.Errorf("Authorization = %q, want empty", (*got)[0].auth)
	}
}

func TestClient_BasicAuth_HalfSetIsNoOp(t *testing.T) {
	// Both fields must be set for auth to fire — mirrors the
	// go2rtcmgr.APIClient behavior so a partially-configured operator
	// doesn't get locked out.
	srv, got := newRecorder(t, http.StatusOK, "")
	c := newClient(srv.URL)
	c.SetBasicAuth("admin", "") // password missing
	_ = c.SetSpotlight(context.Background(), "cam", true)
	if (*got)[0].auth != "" {
		t.Errorf("half-set auth leaked: %q", (*got)[0].auth)
	}
}

// ── Error mapping ───────────────────────────────────────────────────

func TestClient_404_CameraOffline(t *testing.T) {
	// go2rtc emits a body containing "src" or "camera" when the stream
	// lookup fails.
	srv, _ := newRecorder(t, http.StatusNotFound, `{"error":"src not found"}`)
	c := newClient(srv.URL)
	err := c.SetSpotlight(context.Background(), "cam", true)
	if err != ErrCameraOffline {
		t.Errorf("err = %v, want ErrCameraOffline", err)
	}
}

func TestClient_404_ControlNotSupported(t *testing.T) {
	// Bare net/http mux 404 (unknown endpoint) — body is "404 page not
	// found" without our sentinel words.
	srv, _ := newRecorder(t, http.StatusNotFound, "404 page not found")
	c := newClient(srv.URL)
	err := c.SetSpotlight(context.Background(), "cam", true)
	if err != ErrControlNotSupported {
		t.Errorf("err = %v, want ErrControlNotSupported", err)
	}
}

func TestClient_5xx_SurfacesRawError(t *testing.T) {
	srv, _ := newRecorder(t, http.StatusInternalServerError, "boom")
	c := newClient(srv.URL)
	err := c.SetSpotlight(context.Background(), "cam", true)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want to contain 500 and boom", err)
	}
}

func TestClient_EmptyCameraName_Rejected(t *testing.T) {
	c := newClient("http://irrelevant")
	if err := c.SetSpotlight(context.Background(), "", true); err == nil {
		t.Error("expected error for empty camera name")
	}
}

// ── Parsers ─────────────────────────────────────────────────────────

func TestParseNightVisionMode(t *testing.T) {
	cases := []struct {
		in    string
		want  NightVisionMode
		wantOK bool
	}{
		{"on", NightVisionOn, true},
		{"ON", NightVisionOn, true},
		{"1", NightVisionOn, true},
		{"off", NightVisionOff, true},
		{"2", NightVisionOff, true},
		{"auto", NightVisionAuto, true},
		{"AUTO", NightVisionAuto, true},
		{"3", NightVisionAuto, true},
		{"", 0, false},
		{"garbled", 0, false},
		{"0", 0, false}, // 0 is not a valid NV mode in wyzecam
		{"4", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseNightVisionMode(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("got (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestNightVisionMode_String(t *testing.T) {
	cases := map[NightVisionMode]string{
		NightVisionOn:   "on",
		NightVisionOff:  "off",
		NightVisionAuto: "auto",
		NightVisionMode(99): "unknown",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", m, got, want)
		}
	}
}
