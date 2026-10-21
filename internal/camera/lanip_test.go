package camera

import (
	"context"
	"encoding/json"
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
