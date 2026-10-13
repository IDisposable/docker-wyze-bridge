package go2rtcmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func TestAPIClient_BasicAuth_AttachedToEveryRequest(t *testing.T) {
	// Every method that hits go2rtc must forward Basic auth when
	// SetBasicAuth is configured. Missing this on any endpoint would
	// break go2rtc customization for auth-protected setups (#123).
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		// GET /api/streams returns a valid map; others just 200.
		if r.Method == "GET" && r.URL.Path == "/api/streams" && r.URL.Query().Get("src") == "" {
			json.NewEncoder(w).Encode(map[string]*StreamInfo{})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	c.SetBasicAuth("admin", "hunter2")

	ctx := context.Background()
	_, _ = c.ListStreams(ctx)
	_ = c.AddStream(ctx, "cam", "wyze://x")
	_ = c.DeleteStream(ctx, "cam")
	_ = c.DeleteStreamAndStop(ctx, "cam")
	_, _ = c.GetStreamInfo(ctx, "cam")
	_, _ = c.GetSnapshot(ctx, "cam")

	if len(gotAuth) != 6 {
		t.Fatalf("expected 6 requests, got %d", len(gotAuth))
	}
	// Basic YWRtaW46aHVudGVyMg== == "admin:hunter2"
	want := "Basic YWRtaW46aHVudGVyMg=="
	for i, h := range gotAuth {
		if h != want {
			t.Errorf("request[%d] Authorization = %q, want %q", i, h, want)
		}
	}
}

func TestAPIClient_NoAuth_HeaderAbsent(t *testing.T) {
	// Default (no SetBasicAuth) must send zero Authorization headers
	// so we don't perturb an unauthenticated go2rtc.
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]*StreamInfo{})
	}))
	defer server.Close()

	c := NewAPIClient(server.URL, zerolog.Nop())
	_, _ = c.ListStreams(context.Background())
	if got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

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
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "stop-owned-stream"}[stop], func(t *testing.T) {
			allowed := []string{"ffmpeg:front_4k#video=h264#width=1920#height=1080", "rtsp://example.test/path?name=front&quality=sd"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" || r.URL.Query().Get("src") != "test cam&preview" {
					t.Errorf("incorrect deletion request: %s %s", r.Method, r.URL.RawQuery)
				}
				want := ""
				if stop {
					want = "1"
				}
				if r.URL.Query().Get("stop") != want {
					t.Errorf("stop = %q, want %q", r.URL.Query().Get("stop"), want)
				}
				got := r.URL.Query()["if_src"]
				if stop {
					if len(got) != 2 || got[0] != allowed[0] || got[1] != allowed[1] {
						t.Errorf("source whitelist = %q, want %q", got, allowed)
					}
				} else if len(got) != 0 {
					t.Error("ordinary deletion included a source guard")
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			c := NewAPIClient(server.URL, zerolog.Nop())
			var err error
			if stop {
				err = c.DeleteStreamAndStop(context.Background(), "test cam&preview", allowed...)
			} else {
				err = c.DeleteStream(context.Background(), "test cam&preview")
			}
			if err != nil {
				t.Fatalf("DeleteStream: %v", err)
			}
		})
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
