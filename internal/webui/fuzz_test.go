package webui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The three JSON endpoints the WebUI writes to get arbitrary bodies.
// Whatever comes in, the answer is 200 or 400 (never a 500 or a
// panic), and what's stored afterwards is still valid.

func FuzzAPISettingsBody(f *testing.F) {
	f.Add([]byte(`{"scope":"local","subnets":["192.168.4.0/22"],"names":["Pet Cam*"],"macs":[],"models":[],"block":true}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"scope":5}`))
	f.Add([]byte(`{"names":"x"}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Add([]byte(`{"subnets":["1.2.3.4/40"]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		srv, _ := testServer(t)
		w := httptest.NewRecorder()
		srv.handleAPISettings(w, httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body)))
		if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
			t.Fatalf("status %d for %q", w.Code, body)
		}
		if err := srv.camMgr.SetRules(srv.camMgr.Rules()); err != nil {
			t.Fatalf("stored rules no longer valid after %q: %v", body, err)
		}
	})
}

func FuzzAPILayoutBody(f *testing.F) {
	// Any PUT body gets a 200 or a 400, and whatever layout is stored
	// afterwards only holds named cameras with a nonzero quarter turn.
	f.Add([]byte(`{"order":["a","b"],"locked":true,"rotation":{"a":90}}`))
	f.Add([]byte(`{"rotation":{"a":45}}`))
	f.Add([]byte(`{"order":[1]}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, body []byte) {
		srv, _ := testServer(t)
		w := httptest.NewRecorder()
		srv.handleAPILayout(w, httptest.NewRequest("PUT", "/api/layout", bytes.NewReader(body)))
		if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
			t.Fatalf("status %d for %q", w.Code, body)
		}
		for name, deg := range srv.Layout().Rotation {
			if !validRotation(deg) || deg == 0 || name == "" {
				t.Fatalf("invalid rotation stored after %q: %q=%d", body, name, deg)
			}
		}
	})
}

func FuzzAPIPinBody(f *testing.F) {
	// Any body POSTed to a camera's pin action gets a 200 or a 400,
	// never a 500 or a panic.
	f.Add([]byte(`{"ip":"192.168.4.151"}`))
	f.Add([]byte(`{"ip":""}`))
	f.Add([]byte(`{"ip":"garage"}`))
	f.Add([]byte(`{"ip":1}`))
	f.Add([]byte(`x`))
	f.Fuzz(func(t *testing.T, body []byte) {
		srv, _ := testServer(t)
		injectStreamingCamera(srv, "garage")
		w := httptest.NewRecorder()
		srv.handleAPICameraAction(w, httptest.NewRequest("POST", "/api/cameras/garage/pin", bytes.NewReader(body)))
		if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
			t.Fatalf("status %d for %q", w.Code, body)
		}
	})
}
