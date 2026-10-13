package camera

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestLakeExtraYAMLAliasCollisionPreservesOperatorStream(t *testing.T) {
	for _, alias := range []string{"front_4k", "front_lake"} {
		for _, raw := range []string{
			"streams:\n  " + alias + ": rtsp://example.test/operator-camera\n",
			"defaults: &defaults\n  streams:\n    " + alias + ": rtsp://example.test/operator-camera\n<<: *defaults\n",
			"defaults: &defaults\n  " + alias + ": rtsp://example.test/operator-camera\nstreams:\n  <<: *defaults\n",
		} {
			m, api := newLakeTestManager(t)
			ctx := context.Background()
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
			m.cameras["front"] = cam
			m.cfg.Go2RTCExtraYAML = raw
			const source = "rtsp://example.test/operator-camera"
			if err := api.AddStream(ctx, alias, source); err != nil {
				t.Fatal(err)
			}
			m.connectCamera(ctx, cam)
			streams, err := api.ListStreams(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(streams) != 1 || streams[alias] == nil || streams[alias].Producers[0].URL != source || cam.GetState() != StateError {
				t.Fatalf("operator stream %s was replaced by Lake outputs", alias)
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
	m.SetLakeReset(func(name string) {
		closed = name == "front"
		if cam.GetState() != StateOffline {
			t.Error("renamed camera still accepts offers on its old name")
		}
	})
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

func TestLakeReconnectStartsFreshHealthGrace(t *testing.T) {
	m, _ := newLakeTestManager(t)
	cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
	m.cameras["front"] = cam
	now := time.Now()
	for _, stage := range []string{"transport", "native", "preview"} {
		m.lakeHealth["front/"+stage] = lakeHealthState{bytes: 0, lastData: now.Add(-2 * time.Minute)}
	}
	m.connectCamera(context.Background(), cam)
	waiting := &go2rtcmgr.StreamInfo{Consumers: []go2rtcmgr.ConsumerInfo{{}}}
	for _, stage := range []string{"transport", "native", "preview"} {
		if !m.lakeStreamHealthy("front/"+stage, waiting, now) {
			t.Errorf("new %s session inherited the previous stalled timer", stage)
		}
	}
}

func TestLakeDiscoveryRemovalClosesSessionAndOutputs(t *testing.T) {
	for _, filtered := range []bool{false, true} {
		t.Run(fmt.Sprintf("filtered=%t", filtered), func(t *testing.T) {
			m, api := newLakeTestManager(t)
			ctx := context.Background()
			var discoveryMu sync.Mutex
			includeFront := true
			device := func(name, model, mac string) map[string]interface{} {
				return map[string]interface{}{
					"product_type": "Camera", "product_model": model, "nickname": name,
					"mac": mac, "enr": "test", "device_params": map[string]interface{}{"ip": "192.0.2.1", "p2p_id": "test"},
				}
			}
			wyze := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/home_page/get_object_list" {
					http.NotFound(w, r)
					return
				}
				discoveryMu.Lock()
				defer discoveryMu.Unlock()
				devices := []interface{}{device("Other", "HL_CAM4", "OTHER")}
				if includeFront {
					devices = append(devices, device("Front", "HL_PAN4", "FRONT"), device("Legacy", "WYZE_CAKP2JFUS", "LEGACY"))
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": "1", "data": map[string]interface{}{"device_list": devices}})
			}))
			t.Cleanup(wyze.Close)
			m.api = wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
			m.api.WyzeURL = wyze.URL
			m.api.SetAuth(&wyzeapi.AuthState{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)})
			if err := m.Discover(ctx); err != nil {
				t.Fatal(err)
			}
			m.ConnectAll(ctx)
			front, other := m.GetCamera("front"), m.GetCamera("other")
			legacy := m.GetCamera("legacy")
			if front == nil || other == nil || legacy == nil {
				t.Fatal("fake API did not discover all cameras")
			}
			closed := 0
			m.SetLakeReset(func(name string) {
				if name != "front" || front.GetState() != StateOffline {
					t.Error("removed camera still accepts new offers during cleanup")
				}
				closed++
			})
			if filtered {
				m.filter = &Filter{Names: []string{"OTHER"}}
			} else {
				discoveryMu.Lock()
				includeFront = false
				discoveryMu.Unlock()
			}
			if err := m.Discover(ctx); err != nil {
				t.Fatal(err)
			}
			beforeReconnect, err := api.ListStreams(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if m.GetCamera("legacy") != legacy || legacy.GetState() != StateOffline || beforeReconnect["legacy"] == nil {
				t.Fatal("non-Pan removal semantics changed")
			}
			m.ConnectAll(ctx)
			streams, err := api.ListStreams(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"front", "front_lake", "front_4k"} {
				if _, exists := streams[name]; exists {
					t.Errorf("removed camera left %s registered", name)
				}
			}
			if closed != 1 || front.GetState() != StateOffline {
				t.Errorf("cleanup calls=%d, removed camera state=%s", closed, front.GetState())
			}
			if m.GetCamera("other") != other || other.GetState() != StateStreaming || streams["other"] == nil {
				t.Fatal("unrelated camera was changed")
			}
		})
	}
}
