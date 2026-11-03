package camera

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/rs/zerolog"
)

func TestLakePublisherLossUsesDemandedHealthRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		model                       string
		state                       State
		previewDemand, nativeDemand int
		lost                        bool
		wantError                   bool
	}{
		{"preview demanded", "HL_PAN4", StateStreaming, 1, 1, true, true},
		{"native only demanded", "HL_PAN4", StateStreaming, 0, 1, true, true},
		{"idle", "HL_PAN4", StateStreaming, 0, 0, true, false},
		{"no publisher loss", "HL_PAN4", StateStreaming, 1, 1, false, false},
		{"stopped", "HL_PAN4", StateOffline, 1, 1, true, false},
		{"other model", "HL_CAM4", StateStreaming, 1, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := func(readers int) *go2rtcmgr.StreamInfo {
				return &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{ID: 1, BytesRecv: 200}}, Consumers: make([]go2rtcmgr.ConsumerInfo, readers)}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("health observation mutated engine streams")
				}
				_ = json.NewEncoder(w).Encode(map[string]*go2rtcmgr.StreamInfo{
					"front": stream(tc.previewDemand), "front_4k": stream(tc.nativeDemand), "front_lake": stream(1),
					"side": stream(1), "side_4k": stream(1), "side_lake": stream(1),
				})
			}))
			defer srv.Close()
			m := NewManager(&config.Config{}, nil, go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop()), zerolog.Nop())
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: tc.model}, "hd", false, false)
			cam.SetState(tc.state)
			m.cameras["front"] = cam
			side := NewCamera(wyzeapi.CameraInfo{Name: "side", Model: "HL_PAN4"}, "hd", false, false)
			side.SetState(StateStreaming)
			m.cameras["side"] = side
			for _, stage := range []string{"transport", "native", "preview"} {
				m.lakeHealth["front/"+stage] = lakeHealthState{bytes: 100, lastData: time.Now(), required: true}
			}
			calls := 0
			m.SetLakeNeedsReconnect(func(name string) bool {
				if name == "front" {
					calls++
					return tc.lost
				}
				return false
			})
			m.HealthCheck(context.Background())
			if got := cam.GetState() == StateError; got != tc.wantError {
				t.Fatalf("publisher loss with fresh media counters: state=%v, wantError=%t", cam.GetState(), tc.wantError)
			}
			wantErrors := 0
			if tc.wantError {
				wantErrors = 1
			}
			if side.GetState() != StateStreaming || cam.GetErrorCount() != wantErrors {
				t.Fatal("recovery affected another camera or bypassed existing backoff")
			}
			if (tc.state != StateStreaming || tc.model != "HL_PAN4" || tc.previewDemand+tc.nativeDemand == 0) && calls != 0 {
				t.Fatal("publisher-loss provider was consulted for idle, stopped or non-Lake camera")
			}
		})
	}
}

func TestLakePublisherLossReconnectUsesOwnedTeardown(t *testing.T) {
	for _, operatorOutput := range []string{"", "front", "front_4k", "front_lake"} {
		t.Run("operator="+operatorOutput, func(t *testing.T) {
			var mu sync.Mutex
			streams := map[string]string{"other": "rtsp://example.test/other-camera"}
			var events []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodGet:
					data := map[string]*go2rtcmgr.StreamInfo{}
					for name, source := range streams {
						data[name] = &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{ID: 1, URL: source, BytesRecv: 200}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}}
					}
					_ = json.NewEncoder(w).Encode(data)
				case http.MethodPut:
					streams[r.URL.Query().Get("name")] = r.URL.Query().Get("src")
				case http.MethodDelete:
					name := r.URL.Query().Get("src")
					matched := false
					for _, allowed := range r.URL.Query()["if_src"] {
						matched = matched || streams[name] == allowed
					}
					if streams[name] != "" && (!matched || r.URL.Query().Get("stop") != "1") {
						http.Error(w, "source ownership mismatch", http.StatusConflict)
						return
					}
					events = append(events, "remove:"+name)
					delete(streams, name)
				}
			}))
			defer srv.Close()
			api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
			m := NewManager(&config.Config{BridgePort: 5080}, nil, api, zerolog.Nop())
			cam := NewCamera(wyzeapi.CameraInfo{Name: "front", Model: "HL_PAN4"}, "hd", false, false)
			m.cameras["front"] = cam
			lost := false
			m.SetLakeNeedsReconnect(func(name string) bool { return name == "front" && lost })
			m.SetLakeReset(func(name string) {
				lost = false
				mu.Lock()
				events = append(events, "gateway:"+name)
				mu.Unlock()
			})
			ctx := context.Background()
			m.connectCamera(ctx, cam)
			const operatorSource = "rtsp://example.test/operator-camera"
			if operatorOutput != "" {
				if err := api.AddStream(ctx, operatorOutput, operatorSource); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			events = nil
			mu.Unlock()
			lost = true
			m.HealthCheck(ctx)
			if cam.GetState() != StateError || cam.GetErrorCount() != 1 {
				t.Fatal("publisher loss did not enter the existing backoff path")
			}
			cam.mu.Lock()
			cam.LastSeen = time.Now().Add(-time.Hour)
			cam.mu.Unlock()
			m.reconnectErrored(ctx)
			mu.Lock()
			defer mu.Unlock()
			if len(events) == 0 || events[0] != "gateway:front" || lost || streams["other"] != "rtsp://example.test/other-camera" {
				t.Fatal("recovery bypassed gateway-first cleanup or affected another camera")
			}
			if operatorOutput != "" {
				if streams[operatorOutput] != operatorSource || cam.GetState() != StateError {
					t.Fatal("recovery replaced an operator source or ignored its conflict")
				}
			} else if cam.GetState() != StateStreaming || len(streams) != 4 {
				t.Fatal("existing backoff did not recreate one complete owned chain")
			}
		})
	}
}
