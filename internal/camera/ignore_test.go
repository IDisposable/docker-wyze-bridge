package camera

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// go2rtcSpy records stream deletes so tests can prove an ignored
// camera's stream really left go2rtc.
func go2rtcSpy(t *testing.T) (*go2rtcmgr.APIClient, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			mu.Lock()
			deleted = append(deleted, r.URL.Query().Get("src"))
			mu.Unlock()
			w.WriteHeader(200)
		case r.URL.Path == "/api/streams":
			w.Write([]byte("{}"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop()), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), deleted...)
	}
}

// discoveringManager builds a manager whose Wyze API is the two-camera
// mock (front_door AABB01, backyard AABB02) from integration_test.go.
func discoveringManager(t *testing.T) (*Manager, func() []string) {
	t.Helper()
	wyzeSrv := mockWyzeAPIServer(t)
	go2rtcAPI, deleted := go2rtcSpy(t)
	cfg := &config.Config{Quality: "hd", Audio: true, CamOverrides: map[string]config.CamOverride{}, RefreshInterval: time.Hour}
	wapi := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	wapi.SetAuth(&wyzeapi.AuthState{AccessToken: "tok", RefreshToken: "r", ExpiresAt: time.Now().Add(24 * time.Hour)})
	wapi.WyzeURL = wyzeSrv.URL
	return NewManager(cfg, wapi, go2rtcAPI, zerolog.Nop()), deleted
}

func TestIgnore_DropsCameraAndPersists(t *testing.T) {
	// Ignoring a camera takes it out of the registry, deletes its go2rtc
	// stream, sends it offline and saves the list by MAC. The next
	// Discover must not bring it back, and ignoring twice is an error.
	mgr, deleted := discoveringManager(t)
	ctx := context.Background()
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	cam := mgr.GetCamera("backyard")
	if cam == nil {
		t.Fatal("setup: backyard not discovered")
	}
	cam.SetState(StateStreaming)

	var lists [][]wyzeapi.IgnoredCamera
	mgr.OnIgnoreChange(func(l []wyzeapi.IgnoredCamera) { lists = append(lists, l) })
	var removed []string
	mgr.OnCameraRemoved(func(c *Camera, reason string) { removed = append(removed, c.Name()+":"+reason) })
	var states []State
	mgr.OnStateChange(func(_ *Camera, _, n State) { states = append(states, n) })

	entry, err := mgr.Ignore(ctx, "backyard")
	if err != nil {
		t.Fatal(err)
	}
	if entry.MAC != "AABB02" || entry.Nickname != "Backyard" || entry.Model != "WYZE_CAKP2JFUS" || entry.Since.IsZero() {
		t.Errorf("entry = %+v", entry)
	}
	if mgr.GetCamera("backyard") != nil || len(mgr.Cameras()) != 1 {
		t.Error("ignored camera still in the registry")
	}
	if !mgr.IsIgnored("aabb02") || !mgr.IsIgnored(" AABB02 ") {
		t.Error("IsIgnored should match any spelling")
	}
	if d := deleted(); len(d) != 1 || d[0] != "backyard" {
		t.Errorf("go2rtc deletes = %v, want [backyard]", d)
	}
	if len(states) != 1 || states[0] != StateOffline {
		t.Errorf("state transitions = %v, want one to offline", states)
	}
	if len(removed) != 1 || removed[0] != "backyard:ignored" {
		t.Errorf("removed = %v", removed)
	}
	if len(lists) != 1 || len(lists[0]) != 1 || lists[0][0].MAC != "AABB02" {
		t.Errorf("persist callback got %+v", lists)
	}

	// A second discovery must not bring it back, and must keep its
	// nickname fresh in the ignore list.
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if mgr.GetCamera("backyard") != nil {
		t.Error("ignored camera rediscovered")
	}

	// Ignoring again, or ignoring something unknown, is an error.
	if _, err := mgr.Ignore(ctx, "backyard"); !errors.Is(err, ErrUnknownCamera) {
		t.Errorf("second ignore: %v", err)
	}
	if _, err := mgr.Ignore(ctx, "nope"); !errors.Is(err, ErrUnknownCamera) {
		t.Errorf("unknown: %v", err)
	}
}

func TestUnignore_RestoresOnNextDiscover(t *testing.T) {
	// An ignored MAC stays out of discovery (its stored nickname is
	// refreshed from the cloud), and after Unignore the camera comes back
	// through the normal added path on the next Discover.
	mgr, _ := discoveringManager(t)
	ctx := context.Background()
	mgr.SetIgnored([]wyzeapi.IgnoredCamera{{MAC: "aabb02", Nickname: "stale name", Since: time.Now()}})

	var added []string
	mgr.OnCameraAdded(func(c *Camera) { added = append(added, c.Name()) })
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "front_door" {
		t.Errorf("added = %v, want only front_door", added)
	}
	// Discovery refreshed the ignored entry's nickname from the cloud.
	if got := mgr.Ignored(); len(got) != 1 || got[0].MAC != "AABB02" || got[0].Nickname != "Backyard" {
		t.Errorf("ignored = %+v", got)
	}

	var lists [][]wyzeapi.IgnoredCamera
	mgr.OnIgnoreChange(func(l []wyzeapi.IgnoredCamera) { lists = append(lists, l) })
	entry, err := mgr.Unignore("AABB02")
	if err != nil || entry.Nickname != "Backyard" {
		t.Fatalf("Unignore = %+v, %v", entry, err)
	}
	if len(lists) != 1 || len(lists[0]) != 0 {
		t.Errorf("persist callback got %+v, want an empty list", lists)
	}
	if _, err := mgr.Unignore("AABB02"); !errors.Is(err, ErrNotIgnored) {
		t.Errorf("second unignore: %v", err)
	}

	added = nil
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if mgr.GetCamera("backyard") == nil || len(added) != 1 || added[0] != "backyard" {
		t.Errorf("restored camera not re-added: added=%v", added)
	}
}

func TestSetIgnored_NormalizesAndSorts(t *testing.T) {
	// The saved ignore list loads with MACs uppercased, no-MAC entries
	// dropped and a stable nickname-then-MAC order for the WebUI.
	// Loading a new list replaces the old one.
	mgr, _ := newTestManager(t)
	mgr.SetIgnored([]wyzeapi.IgnoredCamera{
		{MAC: "d03f2700ca03", Nickname: "Driveway"},
		{MAC: "", Nickname: "no mac"},
		{MAC: "80482C0D6B02", Nickname: "Garage Cam"},
		{MAC: "D03F2700CA01", Nickname: "Back Yard"},
		{MAC: "AAAAAAAAAAAA"}, // no nickname sorts first (empty string)
	})
	got := mgr.Ignored()
	want := []string{"AAAAAAAAAAAA", "D03F2700CA01", "D03F2700CA03", "80482C0D6B02"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].MAC != w {
			t.Errorf("[%d] = %s, want %s", i, got[i].MAC, w)
		}
	}
	// Installing a new list replaces the old one wholesale.
	mgr.SetIgnored(nil)
	if len(mgr.Ignored()) != 0 || mgr.IsIgnored("D03F2700CA03") {
		t.Error("SetIgnored(nil) should clear")
	}
}

func TestIgnore_NoMAC(t *testing.T) {
	// The ignore list is keyed by MAC, so a camera without one is refused
	// and has to stay in the registry when the ignore fails.
	mgr, _ := newTestManager(t)
	mgr.cameras["ghost"] = NewCamera(wyzeapi.CameraInfo{Name: "ghost"}, "hd", true, false)
	if _, err := mgr.Ignore(context.Background(), "ghost"); err == nil {
		t.Error("a camera without a MAC can't be ignored by MAC")
	}
	if mgr.GetCamera("ghost") == nil {
		t.Error("failed ignore must leave the camera in place")
	}
}

func TestDiscover_RenameFiresRemovedAndAdded(t *testing.T) {
	// A camera renamed in the Wyze app comes back under a new name with
	// the same MAC. The old entry is removed (reason "renamed") and its
	// go2rtc stream deleted, or it would linger as a dead card.
	mgr, deleted := discoveringManager(t)
	ctx := context.Background()
	// Pre-existing entry for the same MAC under the old nickname.
	old := NewCamera(wyzeapi.CameraInfo{Name: "yard", Nickname: "Yard", MAC: "AABB02"}, "hd", true, false)
	mgr.cameras["yard"] = old

	var removed, added []string
	mgr.OnCameraRemoved(func(c *Camera, reason string) { removed = append(removed, c.Name()+":"+reason) })
	mgr.OnCameraAdded(func(c *Camera) { added = append(added, c.Name()) })

	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "yard:renamed" {
		t.Errorf("removed = %v", removed)
	}
	if len(added) != 2 { // front_door + backyard (the renamed one)
		t.Errorf("added = %v", added)
	}
	if d := deleted(); len(d) != 1 || d[0] != "yard" {
		t.Errorf("go2rtc deletes = %v", d)
	}
}
