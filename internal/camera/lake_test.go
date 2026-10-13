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
	"sync/atomic"
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

func TestLakeQualityBeforeEngineAttachment(t *testing.T) {
	m, _ := newLakeTestManager(t)
	m.SetGo2RTCAPI(nil)
	cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
	m.cameras["front"] = cam
	if err := m.SetQuality(context.Background(), "front", "sd"); err != nil || cam.Snapshot().Quality != "sd" {
		t.Fatal("desired preview quality was lost before engine attachment")
	}
}
func TestLakeNativeStallAndIdle(t *testing.T) {
	m, _ := newLakeTestManager(t)
	now := time.Now()
	native := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{BytesRecv: 100}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
	if !m.lakeStreamHealthy("front", native, now, false) {
		t.Fatal("startup incorrectly failed")
	}
	if !m.lakeStreamHealthy("front", native, now.Add(30*time.Second), false) {
		t.Fatal("transient delay failed")
	}
	if m.lakeStreamHealthy("front", native, now.Add(61*time.Second), false) {
		t.Fatal("stalled producer remained healthy")
	}
	native.Producers[0].BytesRecv = 200
	if !m.lakeStreamHealthy("front", native, now.Add(62*time.Second), false) {
		t.Fatal("new bytes did not restore health")
	}
	native.Consumers = nil
	if !m.lakeStreamHealthy("front", native, now.Add(5*time.Minute), false) {
		t.Fatal("idle stream was failed")
	}
	if m.lakeStreamHealthy("front", nil, now, false) {
		t.Fatal("missing native registration was accepted")
	}
}

func TestLakeHealthRequiresDemandedUpstream(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		previewViewers, nativeViewers int
		transportReaders              int
		transportData, nativeData     bool
		wantError                     bool
	}{
		{"frozen preview with no upstream readers", 1, 0, 0, false, false, true},
		{"preview with stalled native worker", 1, 0, 1, true, false, true},
		{"native viewer with stalled transport", 0, 1, 0, false, true, true},
		{"native viewer with live upstream and idle preview", 0, 1, 1, true, true, false},
		{"all outputs idle", 0, 0, 0, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int64
			received.Store(100)
			stream := func(readers int, progressing bool) *go2rtcmgr.StreamInfo {
				var bytes int64
				if progressing {
					bytes = received.Load()
				}
				return &go2rtcmgr.StreamInfo{
					Producers: []go2rtcmgr.ProducerInfo{{BytesRecv: bytes}},
					Consumers: make([]go2rtcmgr.ConsumerInfo, readers),
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]*go2rtcmgr.StreamInfo{
					"front":      stream(tc.previewViewers, true),
					"front_4k":   stream(tc.nativeViewers, tc.nativeData),
					"front_lake": stream(tc.transportReaders, tc.transportData),
				})
			}))
			defer srv.Close()
			api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
			m := NewManager(&config.Config{}, nil, api, zerolog.Nop())
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
			m.cameras["front"] = cam
			cam.SetState(StateStreaming)
			m.HealthCheck(context.Background())
			if cam.GetState() != StateStreaming {
				t.Fatal("required upstream did not receive startup grace")
			}
			// Advance only the existing liveness timers. Preview bytes still
			// grow, reproducing a stale picture encoded as valid H264 packets.
			stalledSince := time.Now().Add(-61 * time.Second)
			for _, stage := range []string{"transport", "native"} {
				var bytes int64
				if stage == "transport" && tc.transportData || stage == "native" && tc.nativeData {
					bytes = 100
				}
				m.lakeHealth["front/"+stage] = lakeHealthState{bytes: bytes, lastData: stalledSince, required: true}
			}
			received.Store(200)
			m.HealthCheck(context.Background())
			if gotError := cam.GetState() == StateError; gotError != tc.wantError {
				t.Fatalf("state=%v, want stalled upstream error=%t", cam.GetState(), tc.wantError)
			}
		})
	}
}

func TestLakeEstablishedUpstreamContinuity(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		beforeIDs, afterIDs   []int64
		before, after         int64
		elapsed               time.Duration
		required, wantHealthy bool
	}{
		{"same producer advances", []int64{1}, []int64{1}, 100, 200, time.Second, true, true},
		{"same producer stalls", []int64{1}, []int64{1}, 100, 100, 61 * time.Second, true, false},
		{"replacement counter overtakes old counter", []int64{1}, []int64{2}, 100, 200, time.Second, true, false},
		{"same producer counter resets", []int64{1}, []int64{1}, 100, 50, time.Second, true, false},
		{"legacy counter resets without identity", []int64{0}, []int64{0}, 100, 50, time.Second, true, false},
		{"live producer disappears", []int64{1}, []int64{0}, 100, 0, time.Second, true, false},
		{"first producer arrives", []int64{0}, []int64{1}, 0, 100, time.Second, true, true},
		{"identity set reorders", []int64{1, 2}, []int64{2, 1}, 100, 200, time.Second, true, true},
		{"identity set changes", []int64{1, 2}, []int64{1, 3}, 100, 200, time.Second, true, false},
		{"preview replacement leaves upstream alone", []int64{1}, []int64{2}, 100, 50, time.Second, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newLakeTestManager(t)
			now := time.Now()
			stream := func(ids []int64, bytes int64) *go2rtcmgr.StreamInfo {
				producers := make([]go2rtcmgr.ProducerInfo, len(ids))
				for i, id := range ids {
					producers[i] = go2rtcmgr.ProducerInfo{ID: id, BytesRecv: bytes / int64(len(ids))}
				}
				return &go2rtcmgr.StreamInfo{Producers: producers, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
			}
			if !m.lakeStreamHealthy("front/transport", stream(tc.beforeIDs, tc.before), now, tc.required) {
				t.Fatal("initial observation did not establish a baseline")
			}
			if healthy := m.lakeStreamHealthy("front/transport", stream(tc.afterIDs, tc.after), now.Add(tc.elapsed), tc.required); healthy != tc.wantHealthy {
				t.Fatalf("healthy=%t, want %t after upstream transition", healthy, tc.wantHealthy)
			}
		})
	}
}

func TestLakeColdProducerChangesDoNotExtendGrace(t *testing.T) {
	m, _ := newLakeTestManager(t)
	now := time.Now()
	stream := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{ID: 1}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
	if !m.lakeStreamHealthy("front/transport", stream, now, true) {
		t.Fatal("cold producer did not receive startup grace")
	}
	stream.Producers[0].ID = 2
	if !m.lakeStreamHealthy("front/transport", stream, now.Add(30*time.Second), true) {
		t.Fatal("cold retry lost its remaining startup grace")
	}
	stream.Producers[0].ID = 3
	if m.lakeStreamHealthy("front/transport", stream, now.Add(61*time.Second), true) {
		t.Fatal("zero-byte producer retries extended startup grace indefinitely")
	}
}

func TestLakeIdleDemandResetsUpstreamBaseline(t *testing.T) {
	m, _ := newLakeTestManager(t)
	now := time.Now()
	stream := &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{ID: 1, BytesRecv: 100}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
	if !m.lakeStreamHealthy("front/transport", stream, now, true) {
		t.Fatal("live upstream was unhealthy")
	}
	// An internal worker may still be draining after both public outputs
	// become idle. Its presence must not pin the next viewing session.
	if !m.lakeStreamHealthy("front/transport", stream, now.Add(30*time.Second), false) {
		t.Fatal("idle output was unhealthy")
	}
	stream.Producers[0] = go2rtcmgr.ProducerInfo{ID: 2, BytesRecv: 50}
	if !m.lakeStreamHealthy("front/transport", stream, now.Add(time.Minute), true) {
		t.Fatal("normal on-demand startup inherited an old producer baseline")
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
	for _, afterStore := range []bool{false, true} {
		for _, failed := range []string{"front_lake", "front_4k", "front"} {
			m, api := newLakeTestManager(t, lakeRegistrationFailure{failed, afterStore})
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
			m.cameras["front"] = cam
			m.connectCamera(context.Background(), cam)
			streams, _ := api.ListStreams(context.Background())
			if len(streams) != 0 || cam.GetState() != StateError {
				t.Fatalf("failed registration left partial outputs: %s, stored=%t", failed, afterStore)
			}
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

func TestLakeOwnedTeardownStopsOutputsAfterGateway(t *testing.T) {
	for _, pan := range []bool{false, true} {
		for _, operation := range []string{"stop", "restart", "reconnect", "rename", "quality"} {
			t.Run(fmt.Sprintf("pan=%t/%s", pan, operation), func(t *testing.T) {
				var mu sync.Mutex
				var events []string
				record := func(event string) {
					mu.Lock()
					defer mu.Unlock()
					events = append(events, event)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodDelete {
						want := ""
						if pan {
							want = "1"
						}
						if r.URL.Query().Get("stop") != want {
							t.Errorf("stop = %q, want %q", r.URL.Query().Get("stop"), want)
						}
						record("remove:" + r.URL.Query().Get("src"))
					}
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				api := go2rtcmgr.NewAPIClient(server.URL, zerolog.Nop())
				m := NewManager(&config.Config{BridgePort: 5080}, nil, api, zerolog.Nop())
				model := "HL_CAM4"
				if pan {
					model = "HL_PAN4"
				}
				cam := NewCamera(wyzeapi.CameraInfo{Name: "front", MAC: "test", Model: model}, "hd", false, false)
				cam.SetState(StateStreaming)
				m.cameras["front"] = cam
				m.SetLakeReset(func(name string) { record("gateway:" + name) })
				ctx := context.Background()
				switch operation {
				case "stop":
					m.StopStream(ctx, "front")
				case "restart":
					m.RestartStream(ctx, "front")
				case "reconnect":
					m.connectCamera(ctx, cam)
				case "rename":
					m.mu.Lock()
					m.reapRenameOrphans(ctx, []wyzeapi.CameraInfo{{Name: "moved", MAC: "test", Model: model}})
					m.mu.Unlock()
				case "quality":
					if err := m.SetQuality(ctx, "front", "sd"); err != nil {
						t.Fatal(err)
					}
				}
				want := "remove:front"
				if !pan && operation == "restart" {
					want = "remove:front,remove:front"
				}
				if pan && operation != "quality" {
					want = "gateway:front,remove:front,remove:front_4k,remove:front_lake"
				}
				mu.Lock()
				got := strings.Join(events, ",")
				mu.Unlock()
				if got != want {
					t.Errorf("teardown = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestLakeCleanupFailureDoesNotStartReplacement(t *testing.T) {
	for _, operation := range []string{"reconnect", "quality"} {
		for _, failed := range []string{"front", "front_4k", "front_lake"} {
			if operation == "quality" && failed != "front" {
				continue
			}
			t.Run(operation+"/"+failed, func(t *testing.T) {
				var mu sync.Mutex
				deletes, puts := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Method == http.MethodDelete {
						deletes++
						if r.URL.Query().Get("src") == failed {
							http.Error(w, "injected cleanup failure", http.StatusServiceUnavailable)
							return
						}
					} else if r.Method == http.MethodPut {
						puts++
					}
					w.WriteHeader(http.StatusOK)
				}))
				defer server.Close()
				api := go2rtcmgr.NewAPIClient(server.URL, zerolog.Nop())
				m := NewManager(&config.Config{BridgePort: 5080}, nil, api, zerolog.Nop())
				cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
				cam.SetState(StateStreaming)
				m.cameras["front"] = cam
				wantDeletes := 3
				if operation == "quality" {
					wantDeletes = 1
					if err := m.SetQuality(context.Background(), "front", "sd"); err == nil {
						t.Fatal("preview cleanup failure was ignored")
					}
				} else {
					m.connectCamera(context.Background(), cam)
					if cam.GetState() != StateError {
						t.Fatal("failed cleanup was not scheduled for retry")
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if deletes != wantDeletes || puts != 0 {
					t.Errorf("cleanup deletes=%d, replacements=%d", deletes, puts)
				}
			})
		}
	}
}

func TestLakeCleanupPreservesOperatorSource(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		for _, output := range []string{"front", "front_4k", "front_lake"} {
			if !replaced && output == "front" {
				continue // The initial collision guard reserves only derived names.
			}
			for _, operation := range []string{"stop", "restart", "rename", "removal", "quality"} {
				if operation == "quality" && output != "front" {
					continue // Quality changes replace only the canonical preview.
				}
				t.Run(fmt.Sprintf("replaced=%t/%s/%s", replaced, output, operation), func(t *testing.T) {
					m, api := newLakeTestManager(t)
					ctx := context.Background()
					cam := NewCamera(wyzeapi.CameraInfo{Name: "front", MAC: "test", Model: "HL_PAN4"}, "hd", false, false)
					m.cameras["front"] = cam
					const operatorSource = "rtsp://example.test/operator-camera"
					if replaced {
						m.connectCamera(ctx, cam)
					} else {
						m.cfg.Go2RTCExtraStreams = output + "=" + operatorSource
					}
					if err := api.AddStream(ctx, output, operatorSource); err != nil {
						t.Fatal(err)
					}
					if !replaced {
						m.connectCamera(ctx, cam)
						if cam.GetState() != StateError {
							t.Fatal("initial operator collision did not prevent registration")
						}
					}
					switch operation {
					case "stop":
						m.StopStream(ctx, "front")
					case "restart":
						m.RestartStream(ctx, "front")
					case "quality":
						if err := m.SetQuality(ctx, "front", "sd"); err == nil || cam.Snapshot().Quality != "hd" {
							t.Fatal("preview ownership conflict changed the quality setting")
						}
					case "rename":
						m.mu.Lock()
						m.reapRenameOrphans(ctx, []wyzeapi.CameraInfo{{Name: "moved", MAC: "test", Model: "HL_PAN4"}})
						m.mu.Unlock()
					case "removal":
						wyze := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": "1", "data": map[string]interface{}{"device_list": []interface{}{}}})
						}))
						defer wyze.Close()
						m.api = wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
						m.api.WyzeURL = wyze.URL
						m.api.SetAuth(&wyzeapi.AuthState{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)})
						if err := m.Discover(ctx); err != nil {
							t.Fatal(err)
						}
					}
					streams, err := api.ListStreams(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if streams[output] == nil || streams[output].Producers[0].URL != operatorSource {
						t.Fatal("cleanup stopped or replaced the operator stream")
					}
				})
			}
		}
	}
}

func TestLakeExternalEngineOperationsPreserveOperatorStreams(t *testing.T) {
	for _, operation := range []string{"connect", "stop", "rename", "removal", "quality"} {
		t.Run(operation, func(t *testing.T) {
			var mu sync.Mutex
			mutations := 0
			streams := map[string]*go2rtcmgr.StreamInfo{}
			for _, name := range []string{"front", "front_4k", "front_lake"} {
				streams[name] = &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{URL: "rtsp://example.test/" + name}}}
			}
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(streams)
					return
				}
				mutations++
				if r.Method == http.MethodDelete {
					// A stock external engine ignores Bridge's opt-in guard fields.
					delete(streams, r.URL.Query().Get("src"))
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer engine.Close()
			api := go2rtcmgr.NewAPIClient(engine.URL, zerolog.Nop())
			m := NewManager(&config.Config{BridgePort: 5080, Go2RTCURL: engine.URL}, nil, api, zerolog.Nop())
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", MAC: "test", Model: "HL_PAN4"}, "hd", false, false)
			cam.SetState(StateStreaming)
			m.cameras["front"] = cam
			closed := 0
			m.SetLakeReset(func(string) { closed++ })
			ctx := context.Background()
			switch operation {
			case "connect":
				m.connectCamera(ctx, cam)
				if cam.GetState() != StateError {
					t.Fatal("external engine was accepted for Lake")
				}
			case "stop":
				m.StopStream(ctx, "front")
			case "rename":
				m.mu.Lock()
				m.reapRenameOrphans(ctx, []wyzeapi.CameraInfo{{Name: "moved", MAC: "test", Model: "HL_PAN4"}})
				m.mu.Unlock()
			case "removal":
				wyze := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": "1", "data": map[string]interface{}{"device_list": []interface{}{}}})
				}))
				defer wyze.Close()
				m.api = wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
				m.api.WyzeURL = wyze.URL
				m.api.SetAuth(&wyzeapi.AuthState{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)})
				if err := m.Discover(ctx); err != nil {
					t.Fatal(err)
				}
			case "quality":
				if err := m.SetQuality(ctx, "front", "sd"); err == nil || cam.Snapshot().Quality != "hd" {
					t.Fatal("external quality change was not rejected before mutation")
				}
			}
			wantClosed := 1
			if operation == "connect" || operation == "quality" {
				wantClosed = 0
			}
			mu.Lock()
			defer mu.Unlock()
			if mutations != 0 || len(streams) != 3 || closed != wantClosed {
				t.Errorf("external mutations=%d, streams=%d, gateway closes=%d", mutations, len(streams), closed)
			}
		})
	}
}

type lakeRegistrationFailure struct {
	name       string
	afterStore bool
}

func newLakeTestManager(t *testing.T, failures ...lakeRegistrationFailure) (*Manager, *go2rtcmgr.APIClient) {
	t.Helper()
	var mu sync.Mutex
	streams := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name := r.URL.Query().Get("name")
		switch r.Method {
		case "PUT":
			for _, failed := range failures {
				if name == failed.name {
					if failed.afterStore {
						// PUT stores the stream before persisting its configuration.
						streams[name] = r.URL.Query().Get("src")
					}
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
			name := r.URL.Query().Get("src")
			if allowed := r.URL.Query()["if_src"]; r.URL.Query().Get("stop") == "1" && len(allowed) > 0 {
				if source, exists := streams[name]; exists {
					matched := false
					for _, expected := range allowed {
						matched = matched || source == expected
					}
					if !matched {
						http.Error(w, "source ownership mismatch", http.StatusConflict)
						return
					}
				}
			}
			delete(streams, name)
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
		if !m.lakeStreamHealthy("front/"+stage, waiting, now, true) {
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
