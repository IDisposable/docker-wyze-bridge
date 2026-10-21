package camera

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// scriptedResolver answers with a fixed Result and records what it was
// asked, so tests can check the manager passed the right target and
// called it only for the protocols that dial a LAN address.
type scriptedResolver struct {
	mu      sync.Mutex
	result  lanresolve.Result
	checkOK bool
	calls   []lanresolve.Target
	checks  []string
}

func (s *scriptedResolver) Resolve(_ context.Context, t lanresolve.Target) lanresolve.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, t)
	res := s.result
	if res.IP == "" {
		res.IP = t.CloudIP
	}
	return res
}

func (s *scriptedResolver) Check(_ context.Context, ip, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, ip)
	return s.checkOK, nil
}

func (s *scriptedResolver) resolveCalls() []lanresolve.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]lanresolve.Target(nil), s.calls...)
}

// recordingGo2RTC is a go2rtc mock that remembers the source URL of
// every stream PUT, which is the whole point of the resolver: the URL
// go2rtc dials has to carry the verified address.
func recordingGo2RTC(t *testing.T) (*go2rtcmgr.APIClient, func(name string) string) {
	t.Helper()
	var mu sync.Mutex
	srcs := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == "PUT":
			mu.Lock()
			srcs[r.URL.Query().Get("name")] = r.URL.Query().Get("src")
			mu.Unlock()
			w.WriteHeader(200)
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			w.WriteHeader(200)
		case r.URL.Path == "/api/streams" && r.Method == "GET":
			mu.Lock()
			result := make(map[string]*go2rtcmgr.StreamInfo)
			for name, src := range srcs {
				result[name] = &go2rtcmgr.StreamInfo{Producers: []go2rtcmgr.ProducerInfo{{URL: src}}}
			}
			mu.Unlock()
			json.NewEncoder(w).Encode(result)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	return api, func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		return srcs[name]
	}
}

func newLanIPTestManager(t *testing.T, r LANResolver) (*Manager, func(name string) string) {
	t.Helper()
	api, srcOf := recordingGo2RTC(t)
	cfg := &config.Config{
		Quality:         "hd",
		Audio:           true,
		CamOverrides:    make(map[string]config.CamOverride),
		RefreshInterval: 30 * time.Minute,
	}
	mgr := NewManager(cfg, wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop()), api, zerolog.Nop())
	if r != nil {
		mgr.SetLANResolver(r)
	}
	return mgr, srcOf
}

func newV3(name, ip string) *Camera {
	return NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		Model: "WYZE_CAKP2JFUS",
		MAC:   "D03F27B081EF",
		LanIP: ip,
		P2PID: "UID01234567890123456",
		ENR:   "enr",
		DTLS:  true,
	}, "hd", true, false)
}

func TestCamera_SetLanIP_OverridesEveryReader(t *testing.T) {
	// A resolved LAN address must reach every reader (GetInfo, Snapshot,
	// StreamURL, StatusJSON) while Info keeps the cloud's raw address.
	// Setting the same override twice is not a change.
	cam := newV3("side_garage", "192.168.4.123")

	if !cam.SetLanIP("192.168.4.151") {
		t.Fatal("first override should report a change")
	}
	if cam.SetLanIP("192.168.4.151") {
		t.Error("same override again should not report a change")
	}
	cloud, resolved := cam.LanIPs()
	if cloud != "192.168.4.123" || resolved != "192.168.4.151" {
		t.Errorf("LanIPs = %s / %s", cloud, resolved)
	}
	if got := cam.GetInfo().LanIP; got != "192.168.4.151" {
		t.Errorf("GetInfo().LanIP = %s", got)
	}
	if got := cam.Snapshot().Info.LanIP; got != "192.168.4.151" {
		t.Errorf("Snapshot().Info.LanIP = %s", got)
	}
	if u := cam.StreamURL(); !strings.HasPrefix(u, "wyze://192.168.4.151?") {
		t.Errorf("StreamURL = %s", u)
	}
	st := cam.StatusJSON()
	if st["ip"] != "192.168.4.151" || st["cloud_ip"] != "192.168.4.123" {
		t.Errorf("StatusJSON ip=%v cloud_ip=%v", st["ip"], st["cloud_ip"])
	}
	// The raw cloud record is untouched.
	if cam.Info.LanIP != "192.168.4.123" {
		t.Errorf("Info.LanIP mutated to %s", cam.Info.LanIP)
	}
}

func TestCamera_SetLanIP_EqualToCloudClears(t *testing.T) {
	// Setting the override back to the cloud's address clears it instead
	// of storing a duplicate, and clearing an empty override is not a
	// change.
	cam := newV3("side_garage", "192.168.4.123")
	cam.SetLanIP("192.168.4.151")
	if !cam.SetLanIP("192.168.4.123") {
		t.Fatal("reverting to the cloud IP is a change")
	}
	if _, resolved := cam.LanIPs(); resolved != "" {
		t.Errorf("override should be cleared, got %q", resolved)
	}
	if cam.SetLanIP("") {
		t.Error("clearing an already-clear override is not a change")
	}
}

func TestCamera_UpdateInfo_KeepsOverride(t *testing.T) {
	// The 30-minute cloud refresh must not undo a resolved address while
	// the cloud still has the stale one, though other fields update.
	// Once the cloud catches up, the override collapses to empty.
	cam := newV3("side_garage", "192.168.4.123")
	cam.SetLanIP("192.168.4.151")

	// The 30-minute cloud refresh still says .123: the override wins.
	info := cam.GetInfo()
	info.LanIP = "192.168.4.123"
	info.FWVersion = "4.36.17.99"
	cam.UpdateInfo(info)
	if got := cam.GetInfo(); got.LanIP != "192.168.4.151" || got.FWVersion != "4.36.17.99" {
		t.Errorf("after refresh: ip=%s fw=%s", got.LanIP, got.FWVersion)
	}

	// The cloud catches up to .151: the effective address is still
	// .151 and the override is redundant until the next resolve clears it.
	info.LanIP = "192.168.4.151"
	cam.UpdateInfo(info)
	if got := cam.GetInfo().LanIP; got != "192.168.4.151" {
		t.Errorf("after cloud caught up: %s", got)
	}
	if cam.SetLanIP("192.168.4.151") {
		t.Error("resolver confirming the (now-matching) cloud IP: effective address unchanged")
	}
	if _, resolved := cam.LanIPs(); resolved != "" {
		t.Errorf("override should collapse to empty once cloud matches, got %q", resolved)
	}
}

func TestConnectCamera_UsesResolvedIPInGo2RTCURL(t *testing.T) {
	// When the cloud's IP is stale, connect hands go2rtc the address the
	// resolver verified and announces the change once. A reconnect passes
	// that address back as LastIP and stays quiet.
	r := &scriptedResolver{result: lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "sweep", Elapsed: 40 * time.Millisecond}}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.123")
	mgr.cameras[cam.Name()] = cam

	var changes []string
	mgr.OnLanIPChange(func(c *Camera, oldIP, newIP, source string) {
		changes = append(changes, c.Name()+":"+oldIP+">"+newIP+":"+source)
	})

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Fatalf("state = %v", cam.GetState())
	}
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.151?") {
		t.Errorf("go2rtc got %q, want the resolved address", src)
	}
	calls := r.resolveCalls()
	if len(calls) != 1 || calls[0].MAC != "D03F27B081EF" || calls[0].CloudIP != "192.168.4.123" || calls[0].LastIP != "" || calls[0].Name != "side_garage" {
		t.Errorf("resolver target = %+v", calls)
	}
	if len(changes) != 1 || changes[0] != "side_garage:192.168.4.123>192.168.4.151:sweep" {
		t.Errorf("changes = %v", changes)
	}

	// Reconnect: same answer, so the last-good address is passed as
	// LastIP and the change callback stays quiet.
	mgr.RestartStream(context.Background(), "side_garage")
	calls = r.resolveCalls()
	if len(calls) != 2 || calls[1].LastIP != "192.168.4.151" {
		t.Errorf("second target = %+v", calls)
	}
	if len(changes) != 1 {
		t.Errorf("callback fired again without a change: %v", changes)
	}
}

func TestConnectCamera_UnverifiedKeepsCloudIP(t *testing.T) {
	// If the resolver can't verify the camera, go2rtc gets the cloud's
	// address untouched and no change is announced, so it's never worse
	// than the bridge was before the resolver.
	r := &scriptedResolver{result: lanresolve.Result{Verified: false, Source: "unverified"}}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.123")
	mgr.cameras[cam.Name()] = cam
	fired := false
	mgr.OnLanIPChange(func(*Camera, string, string, string) { fired = true })

	mgr.connectCamera(context.Background(), cam)

	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.123?") {
		t.Errorf("go2rtc got %q, want the cloud address untouched", src)
	}
	if fired {
		t.Error("no change callback for an unverified result")
	}
}

func TestConnectCamera_UnverifiedKeepsEarlierOverride(t *testing.T) {
	// A camera that was corrected earlier and is now unreachable keeps
	// the corrected address rather than snapping back to the cloud's.
	r := &scriptedResolver{result: lanresolve.Result{Verified: false, Source: "unverified"}}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.123")
	cam.SetLanIP("192.168.4.151")
	mgr.cameras[cam.Name()] = cam

	mgr.connectCamera(context.Background(), cam)

	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.151?") {
		t.Errorf("go2rtc got %q, want the earlier verified address", src)
	}
}

func TestConnectCamera_CloudCaughtUp_ClearsOverride(t *testing.T) {
	// Once the cloud reports the address an earlier override held, the
	// override is cleared, and since the effective address didn't move
	// no change callback fires.
	r := &scriptedResolver{result: lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "cloud"}}
	mgr, _ := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.151") // cloud now reports the right address
	cam.SetLanIP("192.168.4.151")                // ...which an earlier override already held
	mgr.cameras[cam.Name()] = cam
	var changes int
	mgr.OnLanIPChange(func(*Camera, string, string, string) { changes++ })

	mgr.connectCamera(context.Background(), cam)

	if _, resolved := cam.LanIPs(); resolved != "" {
		t.Errorf("override should be cleared once the cloud agrees, got %q", resolved)
	}
	if changes != 0 {
		t.Errorf("effective address didn't change; callback fired %d times", changes)
	}
}

func TestConnectCamera_ResolverOnlyForTUTK(t *testing.T) {
	// Only TUTK streams dial a LAN address, so doorbells, WebRTC cameras
	// and a V3 promoted to WebRTC must never call the resolver.
	r := &scriptedResolver{result: lanresolve.Result{IP: "10.9.9.9", Verified: true, Source: "sweep"}}
	mgr, _ := newLanIPTestManager(t, r)

	doorbell := NewCamera(wyzeapi.CameraInfo{Name: "front_door", Model: "GW_DBD", MAC: "GW_DBD_80482C0D6B01"}, "hd", true, false)
	window := NewCamera(wyzeapi.CameraInfo{Name: "window", Model: "GW_WC", MAC: "AABBCCDDEEFF", LanIP: "10.0.0.5"}, "hd", true, false)
	promoted := newV3("v4", "10.0.0.6")
	promoted.SetForceWebRTC(true)
	for _, c := range []*Camera{doorbell, window, promoted} {
		mgr.cameras[c.Name()] = c
		mgr.connectCamera(context.Background(), c)
	}
	if calls := r.resolveCalls(); len(calls) != 0 {
		t.Errorf("resolver called for non-TUTK cameras: %+v", calls)
	}
}

func TestConnectCamera_NoResolverIsNoOp(t *testing.T) {
	// With no resolver attached (the default), connect uses the cloud's
	// address the way it always did.
	mgr, srcOf := newLanIPTestManager(t, nil)
	cam := newV3("side_garage", "192.168.4.123")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(context.Background(), cam)
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.123?") {
		t.Errorf("go2rtc got %q", src)
	}
}

func TestSetQuality_KeepsResolvedIP(t *testing.T) {
	// Changing quality re-registers the stream, and the new URL has to
	// keep the resolved LAN address instead of the cloud's stale one.
	r := &scriptedResolver{result: lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "neigh"}}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.123")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(context.Background(), cam)

	if err := mgr.SetQuality(context.Background(), "side_garage", "sd"); err != nil {
		t.Fatal(err)
	}
	src := srcOf("side_garage")
	if !strings.HasPrefix(src, "wyze://192.168.4.151?") || !strings.Contains(src, "subtype=sd") {
		t.Errorf("after SetQuality go2rtc got %q", src)
	}
}

// --- periodic recheck ---------------------------------------------------

func (s *scriptedResolver) checkCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.checks...)
}

// setResult swaps the scripted answer between phases of a test.
func (s *scriptedResolver) setResult(res lanresolve.Result, checkOK bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = res
	s.checkOK = checkOK
}

// streamingV3 registers a camera through the normal connect path so it
// sits in StateStreaming with a go2rtc entry, exactly as in production.
func streamingV3(t *testing.T, mgr *Manager, name, ip string) *Camera {
	t.Helper()
	cam := newV3(name, ip)
	mgr.cameras[name] = cam
	mgr.connectCamera(context.Background(), cam)
	if cam.GetState() != StateStreaming {
		t.Fatalf("%s: state = %v after connect", name, cam.GetState())
	}
	return cam
}

func TestRecheckLanIPs_StillAnswering_NothingHappens(t *testing.T) {
	// A streaming camera that still answers at its address costs one
	// Check: no resolve, and its go2rtc stream is left as it was.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "side_garage", "192.168.4.151")
	before := srcOf("side_garage")
	resolves := len(r.resolveCalls())

	mgr.RecheckLanIPs(context.Background())

	if got := r.checkCalls(); len(got) != 1 || got[0] != "192.168.4.151" {
		t.Errorf("checks = %v, want one for the current address", got)
	}
	if len(r.resolveCalls()) != resolves {
		t.Error("a camera that still answers must not be re-resolved")
	}
	if srcOf("side_garage") != before || cam.GetState() != StateStreaming {
		t.Error("stream should be untouched")
	}
}

func TestRecheckLanIPs_LiveVideoIsNotProbed(t *testing.T) {
	// Bytes arriving from the address beat an ARP reply, so a camera
	// with live video is skipped even though it's streaming TUTK.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: false}
	mgr, _ := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "side_garage", "192.168.4.151")
	now := time.Now()
	cam.observeFlow(true, 1, 100, now)
	cam.observeFlow(true, 1, 200, now.Add(30*time.Second))
	if cam.Snapshot().Video != VideoLive {
		t.Fatalf("setup: video = %v", cam.Snapshot().Video)
	}
	resolves := len(r.resolveCalls())
	mgr.RecheckLanIPs(context.Background())
	if len(r.checkCalls()) != 0 || len(r.resolveCalls()) != resolves {
		t.Errorf("live camera was rechecked: checks=%v resolves=%d", r.checkCalls(), len(r.resolveCalls())-resolves)
	}
}

func TestRecheckLanIPs_Moved_ReRegistersWithNewAddress(t *testing.T) {
	// A camera that took a new lease mid-run is re-registered with go2rtc
	// at the new address without flapping its state, and the next
	// recheck probes the corrected address.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "side_garage", "192.168.4.123")
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.123?") {
		t.Fatalf("setup: go2rtc has %q", src)
	}

	var changes []string
	mgr.OnLanIPChange(func(c *Camera, oldIP, newIP, source string) {
		changes = append(changes, oldIP+">"+newIP+":"+source)
	})
	var states []State
	mgr.OnStateChange(func(_ *Camera, _, newState State) { states = append(states, newState) })

	// The camera took a new lease: it no longer answers at .123 and the
	// resolver finds it at .151.
	r.setResult(lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "sweep"}, false)
	mgr.RecheckLanIPs(context.Background())

	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.151?") {
		t.Errorf("go2rtc should have been re-registered with .151, has %q", src)
	}
	if len(changes) != 1 || changes[0] != "192.168.4.123>192.168.4.151:sweep" {
		t.Errorf("changes = %v", changes)
	}
	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want streaming", cam.GetState())
	}
	if len(states) != 0 {
		t.Errorf("re-registration should not flap the state machine, saw %v", states)
	}
	// The next recheck uses the corrected address.
	r.setResult(lanresolve.Result{Verified: true, Source: "cloud"}, true)
	mgr.RecheckLanIPs(context.Background())
	if got := r.checkCalls(); got[len(got)-1] != "192.168.4.151" {
		t.Errorf("recheck after move asked about %s, want 192.168.4.151", got[len(got)-1])
	}
}

func TestRecheckLanIPs_NotAnsweringAndNotFound_LeftAlone(t *testing.T) {
	// Battery camera asleep, or the camera is simply off: Check fails,
	// the resolver comes back unverified, and nothing changes — no
	// re-registration, no state flap, no callback.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "yard_cam", "192.168.4.140")
	before := srcOf("yard_cam")
	fired := 0
	mgr.OnLanIPChange(func(*Camera, string, string, string) { fired++ })

	r.setResult(lanresolve.Result{Verified: false, Source: "unverified"}, false)
	mgr.RecheckLanIPs(context.Background())

	if srcOf("yard_cam") != before {
		t.Error("stream re-registered although the address is unknown")
	}
	if fired != 0 || cam.GetState() != StateStreaming {
		t.Errorf("fired=%d state=%v", fired, cam.GetState())
	}
}

func TestRecheckLanIPs_SkipsNonStreamingNonTUTKAndCheckErrors(t *testing.T) {
	// Only streaming TUTK cameras are rechecked: errored and offline
	// cameras and WebRTC doorbells get no Check at all.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)

	streaming := streamingV3(t, mgr, "streaming", "10.0.0.1")
	errored := newV3("errored", "10.0.0.2")
	errored.SetState(StateError)
	offline := newV3("offline", "10.0.0.3")
	doorbell := NewCamera(wyzeapi.CameraInfo{Name: "bell", Model: "GW_DBD", MAC: "GW_DBD_80482C0D6B01"}, "hd", true, false)
	doorbell.SetState(StateStreaming)
	for _, c := range []*Camera{errored, offline, doorbell} {
		mgr.cameras[c.Name()] = c
	}

	mgr.RecheckLanIPs(context.Background())

	if got := r.checkCalls(); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("checks = %v, want only the streaming TUTK camera", got)
	}
	_ = streaming
}

func TestRecheckLanIPs_NoResolverOrGo2RTC_NoOp(t *testing.T) {
	// The recheck needs both a resolver and go2rtc. Missing either one,
	// it returns without panicking or probing anything.
	mgr, _ := newLanIPTestManager(t, nil)
	streamingV3(t, mgr, "cam", "10.0.0.1")
	mgr.RecheckLanIPs(context.Background()) // no resolver: must not panic

	r := &scriptedResolver{checkOK: true}
	mgr.SetLANResolver(r)
	mgr.SetGo2RTCAPI(nil)
	mgr.RecheckLanIPs(context.Background())
	if len(r.checkCalls()) != 0 {
		t.Error("recheck ran without go2rtc attached")
	}
}

// erroringResolver's Check always fails, as it would with no engine.
type erroringResolver struct{ scriptedResolver }

func (e *erroringResolver) Check(context.Context, string, string) (bool, error) {
	return false, context.DeadlineExceeded
}

func TestRecheckLanIPs_CheckError_Skips(t *testing.T) {
	// When Check itself fails (no ARP engine, say), the camera is skipped
	// instead of falling through to a resolve that could move it.
	r := &erroringResolver{scriptedResolver{result: lanresolve.Result{Verified: false, Source: "unverified"}}}
	mgr, srcOf := newLanIPTestManager(t, r)
	streamingV3(t, mgr, "cam", "10.0.0.1")
	resolves := len(r.resolveCalls())
	// From here on the resolver would move the camera if it were asked.
	r.setResult(lanresolve.Result{IP: "10.0.0.9", Verified: true, Source: "sweep"}, false)

	mgr.RecheckLanIPs(context.Background())

	if len(r.resolveCalls()) != resolves {
		t.Error("a Check error should skip the camera, not resolve it")
	}
	if src := srcOf("cam"); !strings.HasPrefix(src, "wyze://10.0.0.1?") {
		t.Errorf("stream changed: %q", src)
	}
}

func TestRecheckLanIPs_ManyCamerasInParallel(t *testing.T) {
	// The recheck fans out one goroutine per camera. All 16 cameras must
	// get checked, and -race catches any unsafe shared state.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	for i := 0; i < 16; i++ {
		streamingV3(t, mgr, "cam"+string(rune('a'+i)), "10.0.0."+string(rune('1'+i%9)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mgr.RecheckLanIPs(ctx) // -race verifies the fan-out
	if got := len(r.checkCalls()); got != 16 {
		t.Errorf("checks = %d, want 16", got)
	}
}

func TestRunDiscoveryLoop_RecheckTicks(t *testing.T) {
	// A short LAN_IP_RECHECK makes the loop call RecheckLanIPs; the
	// scripted resolver records the Check as proof. Discovery itself
	// fails (no Wyze API) and that's fine — the loop keeps ticking.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	mgr.cfg.LanIPRecheck = 20 * time.Millisecond
	streamingV3(t, mgr, "cam", "10.0.0.1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.RunDiscoveryLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(r.checkCalls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(r.checkCalls()) == 0 {
		t.Error("recheck never ran from the discovery loop")
	}
}

func TestRunDiscoveryLoop_RecheckDisabled(t *testing.T) {
	// LAN_IP_RECHECK=0 turns the periodic recheck off, so the discovery
	// loop runs without ever probing a camera.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	mgr.cfg.LanIPRecheck = 0
	streamingV3(t, mgr, "cam", "10.0.0.1")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	mgr.RunDiscoveryLoop(ctx)
	if len(r.checkCalls()) != 0 {
		t.Error("LAN_IP_RECHECK=0 should disable the recheck ticker")
	}
}

func TestLanStatus_FollowsResolverAndRecheck(t *testing.T) {
	// The detail page and /api/cameras show how the resolver did: the
	// rung that verified the address, when, and how many sweeps missed.
	r := &scriptedResolver{result: lanresolve.Result{IP: "192.168.4.151", Verified: true, Source: "sweep"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	cam := newV3("side_garage", "192.168.4.123")
	mgr.cameras[cam.Name()] = cam
	if src, at, misses := cam.LanStatus(); src != "" || !at.IsZero() || misses != 0 {
		t.Fatalf("fresh camera: %q %v %d", src, at, misses)
	}

	mgr.connectCamera(context.Background(), cam)
	src, at, misses := cam.LanStatus()
	if src != "sweep" || at.IsZero() || misses != 0 {
		t.Errorf("after verify: %q %v %d", src, at, misses)
	}
	js := cam.StatusJSON()
	if js["lan_ip_source"] != "sweep" || js["lan_sweep_misses"] != 0 || js["lan_ip_verified_at"].(time.Time).IsZero() {
		t.Errorf("StatusJSON: source=%v misses=%v at=%v", js["lan_ip_source"], js["lan_sweep_misses"], js["lan_ip_verified_at"])
	}

	// Recheck still answers: the timestamp moves, the rung stays.
	time.Sleep(2 * time.Millisecond)
	mgr.RecheckLanIPs(context.Background())
	src2, at2, _ := cam.LanStatus()
	if src2 != "sweep" || !at2.After(at) {
		t.Errorf("after recheck: %q %v (was %v)", src2, at2, at)
	}

	// Gone missing: three sweeps failed. History stays so the page can
	// say "last verified ... via sweep", misses are reported.
	r.setResult(lanresolve.Result{Verified: false, Source: "unverified", Misses: 3}, false)
	mgr.RestartStream(context.Background(), "side_garage")
	src3, at3, misses3 := cam.LanStatus()
	if src3 != "sweep" || !at3.Equal(at2) || misses3 != 3 {
		t.Errorf("after misses: %q %v %d", src3, at3, misses3)
	}
	snap := cam.Snapshot()
	if snap.LanSource != "sweep" || snap.LanMisses != 3 || !snap.LanVerifiedAt.Equal(at2) {
		t.Errorf("snapshot: %+v", snap)
	}

	// Back again at the cloud address: misses reset.
	r.setResult(lanresolve.Result{IP: "192.168.4.123", Verified: true, Source: "cloud"}, true)
	mgr.RestartStream(context.Background(), "side_garage")
	if src, _, misses := cam.LanStatus(); src != "cloud" || misses != 0 {
		t.Errorf("after return: %q %d", src, misses)
	}

	// A camera nothing ever verified that the recheck catches gets the
	// recheck as its source, so the page doesn't say "never".
	late := newV3("attic", "192.168.4.60")
	late.Info.MAC = "D03F27B081F0"
	mgr.cameras["attic"] = late
	late.SetState(StateStreaming)
	mgr.RecheckLanIPs(context.Background())
	if src, at, _ := late.LanStatus(); src != "recheck" || at.IsZero() {
		t.Errorf("recheck-only camera: %q %v", src, at)
	}
}

func TestLanAbsent_AfterTwoMissesAndStatusHookFires(t *testing.T) {
	// A camera never verified this run reads as not on this LAN after two
	// sweep misses (not one) and clears once found. OnLanStatus fires on
	// every outcome so the dashboard hint follows along.
	r := &scriptedResolver{result: lanresolve.Result{Verified: false, Source: "unverified", Misses: 1}}
	mgr, _ := newLanIPTestManager(t, r)
	cam := newV3("attic", "192.168.4.60")
	mgr.cameras[cam.Name()] = cam
	var fired []string
	mgr.OnLanStatus(func(c *Camera) {
		snap := c.Snapshot()
		fired = append(fired, fmt.Sprintf("%s:%d:%v", c.Name(), snap.LanMisses, snap.LanAbsent))
	})

	mgr.connectCamera(context.Background(), cam)
	if cam.Snapshot().LanAbsent || cam.StatusJSON()["lan_absent"] != false {
		t.Error("one miss is not absent yet")
	}
	r.setResult(lanresolve.Result{Verified: false, Source: "unverified", Misses: 2}, false)
	mgr.RestartStream(context.Background(), "attic")
	if !cam.Snapshot().LanAbsent || cam.StatusJSON()["lan_absent"] != true {
		t.Error("two misses should read as absent")
	}
	// Found again: the verdict clears and the hook says so.
	r.setResult(lanresolve.Result{IP: "192.168.4.60", Verified: true, Source: "cloud"}, true)
	mgr.RestartStream(context.Background(), "attic")
	if cam.Snapshot().LanAbsent {
		t.Error("a verified camera is not absent")
	}
	if want := "attic:1:false,attic:2:true,attic:0:false"; strings.Join(fired, ",") != want {
		t.Errorf("status hook fired %v, want %s", fired, want)
	}
}
