package gwell

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

func TestAPIClient_Register_HappyPath(t *testing.T) {
	var gotBody RegisterRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cameras" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		resp := RegisterResponse{
			Name:    gotBody.Name,
			RTSPURL: "rtsp://127.0.0.1:8564/" + gotBody.Name,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, zerolog.Nop())
	out, err := c.Register(context.Background(), RegisterRequest{
		Name:        "front-door",
		MAC:         "AA:BB:CC:DD:EE:FF",
		ENR:         "e-n-r",
		Model:       "GW_GC1",
		AccessToken: "tok",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if out.Name != "front-door" {
		t.Errorf("Name = %q", out.Name)
	}
	if out.RTSPURL != "rtsp://127.0.0.1:8564/front-door" {
		t.Errorf("RTSPURL = %q", out.RTSPURL)
	}
	if gotBody.AccessToken != "tok" || gotBody.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("wrong body sent: %+v", gotBody)
	}
}

func TestAPIClient_Register_RejectsEmptyToken(t *testing.T) {
	c := NewAPIClient("http://unused", zerolog.Nop())
	_, err := c.Register(context.Background(), RegisterRequest{Name: "x"})
	if err == nil {
		t.Fatal("expected ErrNoAuth")
	}
	if err != ErrNoAuth {
		t.Errorf("err = %v, want ErrNoAuth", err)
	}
}

func TestAPIClient_Register_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "camera not found in cloud", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, zerolog.Nop())
	_, err := c.Register(context.Background(), RegisterRequest{
		Name: "x", AccessToken: "tok",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention 502: %v", err)
	}
}

func TestAPIClient_Unregister_404IsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, zerolog.Nop())
	if err := c.Unregister(context.Background(), "gone"); err != nil {
		t.Errorf("404 should be no-op, got %v", err)
	}
}

func TestAPIClient_Unregister_HappyPath(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		seen = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, zerolog.Nop())
	if err := c.Unregister(context.Background(), "back-yard"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if seen != "/cameras/back-yard" {
		t.Errorf("URL path = %q", seen)
	}
}

func TestAPIClient_ListCameras(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]CameraStatus{
			{Name: "og1", MAC: "AA", State: "streaming", RTSPURL: "rtsp://127.0.0.1:8564/og1"},
			{Name: "og2", MAC: "BB", State: "connecting"},
		})
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, zerolog.Nop())
	got, err := c.ListCameras(context.Background())
	if err != nil {
		t.Fatalf("ListCameras: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].State != "streaming" {
		t.Errorf("state = %q", got[0].State)
	}
}
