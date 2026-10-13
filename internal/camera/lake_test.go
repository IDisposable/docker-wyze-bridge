package camera

import (
	"context"
	"encoding/json"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/rs/zerolog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLakeOutputsAndQuality(t *testing.T) {
	m, api := newLakeTestManager(t)
	cam := NewCamera(wyzeapi.CameraInfo{Name: "front", MAC: "test", Model: "HL_PAN4"}, "hd", false, false)
	m.cameras["front"] = cam
	m.connectCamera(context.Background(), cam)
	streams, e := api.ListStreams(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(streams["front_lake"].Producers[0].URL, "webrtc:ws://127.0.0.1:") {
		t.Fatal("transport did not use integrated signaling")
	}
	if !strings.HasSuffix(streams["front_lake"].Producers[0].URL, "#format=wyze-lake") {
		t.Fatal("transport did not request its dedicated receive window")
	}
	if streams["front_4k"].Producers[0].URL != "ffmpeg:front_lake#video=copy#input=wyze_lake_receive" {
		t.Fatal("native output bypassed packet ordering")
	}
	if !strings.Contains(streams["front"].Producers[0].URL, "ffmpeg:front_4k#video=h264#width=1920#height=1080") {
		t.Fatal("preview did not consume the native output")
	}
	native := streams["front_4k"].Producers[0].URL
	raw := streams["front_lake"].Producers[0].URL
	if e := m.SetQuality(context.Background(), "front", "sd"); e != nil {
		t.Fatal(e)
	}
	streams, _ = api.ListStreams(context.Background())
	if streams["front_4k"].Producers[0].URL != native {
		t.Fatal("preview quality replaced native output")
	}
	if streams["front_lake"].Producers[0].URL != raw {
		t.Fatal("preview quality replaced the shared transport")
	}
	if !strings.Contains(streams["front"].Producers[0].URL, "#width=640#height=360") {
		t.Fatal("SD preview routing was lost")
	}
	stopped := false
	m.SetLakeReset(func(name string) {
		if name == "front" {
			if cam.GetState() != StateOffline {
				t.Error("stop accepted new offers while closing the session")
			}
			stopped = true
		}
	})
	m.StopStream(context.Background(), "front")
	streams, _ = api.ListStreams(context.Background())
	if _, ok := streams["front_4k"]; ok {
		t.Fatal("native stream left active after stop")
	}
	if _, ok := streams["front_lake"]; ok {
		t.Fatal("raw transport left active after stop")
	}
	if !stopped {
		t.Fatal("gateway session was not closed")
	}
}
func TestLakeNativeStallAndIdle(t *testing.T) {
	m, _ := newLakeTestManager(t)
	now := time.Now()
	native := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{BytesRecv: 100}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
	if !m.lakeStreamHealthy("front", native, now) {
		t.Fatal("startup incorrectly failed")
	}
	if !m.lakeStreamHealthy("front", native, now.Add(30*time.Second)) {
		t.Fatal("transient delay failed")
	}
	if m.lakeStreamHealthy("front", native, now.Add(61*time.Second)) {
		t.Fatal("stalled producer remained healthy")
	}
	native.Producers[0].BytesRecv = 200
	if !m.lakeStreamHealthy("front", native, now.Add(62*time.Second)) {
		t.Fatal("new bytes did not restore health")
	}
	native.Consumers = nil
	if !m.lakeStreamHealthy("front", native, now.Add(5*time.Minute)) {
		t.Fatal("idle stream was failed")
	}
	if m.lakeStreamHealthy("front", nil, now) {
		t.Fatal("missing native registration was accepted")
	}
}
func TestLakeAliasCollision(t *testing.T) {
	for _, alias := range []string{"front_4k", "front_lake"} {
		for _, extra := range []bool{false, true} {
			m, api := newLakeTestManager(t)
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
			m.cameras["front"] = cam
			if extra {
				m.cfg.Go2RTCExtraStreams = alias + "=rtsp://example.test/other"
			} else {
				m.cameras[alias] = NewCamera(wyzeapi.CameraInfo{Name: alias, Model: "HL_CAM4"}, "hd", false, false)
			}
			m.connectCamera(context.Background(), cam)
			streams, _ := api.ListStreams(context.Background())
			if len(streams) != 0 || cam.GetState() != StateError {
				t.Fatalf("alias collision overwrote a stream: %s, extra=%t", alias, extra)
			}
		}
	}
}

func TestLakeFailedRegistrationCleansUp(t *testing.T) {
	for _, failed := range []string{"front_lake", "front_4k", "front"} {
		m, api := newLakeTestManager(t, failed)
		cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
		m.cameras["front"] = cam
		m.connectCamera(context.Background(), cam)
		streams, _ := api.ListStreams(context.Background())
		if len(streams) != 0 || cam.GetState() != StateError {
			t.Fatalf("failed registration left partial outputs: %s", failed)
		}
	}
}

func TestLakeExternalEngineFailsBeforeRegistration(t *testing.T) {
	m, api := newLakeTestManager(t)
	m.cfg.Go2RTCURL = "http://example.test:1984"
	cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
	m.cameras["front"] = cam
	m.connectCamera(context.Background(), cam)
	streams, _ := api.ListStreams(context.Background())
	if len(streams) != 0 || cam.GetState() != StateError {
		t.Fatal("external engine was reported as a working integrated receiver")
	}
}

func TestLakeRenameClosesAliasesAndSession(t *testing.T) {
	m, api := newLakeTestManager(t)
	cam := NewCamera(wyzeapi.CameraInfo{Name: "front", MAC: "test", Model: "HL_PAN4"}, "hd", false, false)
	m.cameras["front"] = cam
	m.connectCamera(context.Background(), cam)
	closed := false
	m.SetLakeReset(func(name string) { closed = name == "front" })
	m.mu.Lock()
	m.reapRenameOrphans(context.Background(), []wyzeapi.CameraInfo{{Name: "moved", MAC: "test", Model: "HL_PAN4"}})
	m.mu.Unlock()
	streams, _ := api.ListStreams(context.Background())
	if len(streams) != 0 || !closed || m.GetCamera("front") != nil {
		t.Fatal("rename left an old output or session active")
	}
}

func newLakeTestManager(t *testing.T, failedNames ...string) (*Manager, *go2rtcmgr.APIClient) {
	t.Helper()
	var mu sync.Mutex
	streams := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name := r.URL.Query().Get("name")
		switch r.Method {
		case "PUT":
			for _, failed := range failedNames {
				if name == failed {
					http.Error(w, "injected registration failure", http.StatusServiceUnavailable)
					return
				}
			}
			if strings.Contains(r.URL.Query().Get("src"), " ") {
				http.Error(w, "streams: source with spaces may be insecure", http.StatusBadRequest)
				return
			}
			streams[name] = r.URL.Query().Get("src")
		case "DELETE":
			delete(streams, r.URL.Query().Get("src"))
		case "GET":
			data := map[string]*go2rtcmgr.StreamInfo{}
			for name, url := range streams {
				data[name] = &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{URL: url}}}
			}
			_ = json.NewEncoder(w).Encode(data)
		}
	}))
	t.Cleanup(srv.Close)
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	return NewManager(&config.Config{BridgePort: 5080}, nil, api, zerolog.Nop()), api
}
