package go2rtcmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func TestAPIClient_ListStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streams" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != "GET" {
			t.Errorf("method = %q", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]*StreamInfo{
			"front_door": {
				Producers: []ProducerInfo{{URL: "wyze://1.2.3.4"}},
			},
			"backyard": {},
		})
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	streams, err := c.ListStreams(context.Background())
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Errorf("streams = %d, want 2", len(streams))
	}
	if len(streams["front_door"].Producers) != 1 {
		t.Error("front_door should have 1 producer")
	}
}

func TestAPIClient_AddStream(t *testing.T) {
	var gotName, gotSrc string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		gotName = r.URL.Query().Get("name")
		gotSrc = r.URL.Query().Get("src")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	err := c.AddStream(context.Background(), "test_cam", "wyze://1.2.3.4?uid=X")
	if err != nil {
		t.Fatalf("AddStream: %v", err)
	}
	if gotName != "test_cam" {
		t.Errorf("name = %q", gotName)
	}
	if gotSrc != "wyze://1.2.3.4?uid=X" {
		t.Errorf("src = %q", gotSrc)
	}
}

func TestAPIClient_DeleteStream(t *testing.T) {
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	err := c.DeleteStream(context.Background(), "test_cam")
	if err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
}

func TestAPIClient_HasActiveProducer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]*StreamInfo{
			"active_cam": {
				Producers: []ProducerInfo{{URL: "wyze://1.2.3.4"}},
			},
			"idle_cam": {},
		})
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())

	active, err := c.HasActiveProducer(context.Background(), "active_cam")
	if err != nil {
		t.Fatalf("HasActiveProducer: %v", err)
	}
	if !active {
		t.Error("active_cam should have active producer")
	}

	idle, err := c.HasActiveProducer(context.Background(), "idle_cam")
	if err != nil {
		t.Fatal(err)
	}
	if idle {
		t.Error("idle_cam should not have active producer")
	}

	missing, err := c.HasActiveProducer(context.Background(), "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Error("nonexistent should not have active producer")
	}
}

func TestAPIClient_GetSnapshot(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic bytes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("src") != "test_cam" {
			t.Errorf("src = %q", r.URL.Query().Get("src"))
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	data, err := c.GetSnapshot(context.Background(), "test_cam")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(data) != len(jpegData) {
		t.Errorf("data length = %d, want %d", len(data), len(jpegData))
	}
}

func TestAPIClient_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())

	_, err := c.ListStreams(context.Background())
	if err == nil {
		t.Error("expected error for 500 response")
	}

	err = c.AddStream(context.Background(), "x", "y")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestAPIClient_ConnectionRefused(t *testing.T) {
	c := NewAPIClient("http://localhost:19999", zerolog.Nop())

	_, err := c.ListStreams(context.Background())
	if err == nil {
		t.Error("expected error for connection refused")
	}
}
