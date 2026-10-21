package camera

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// putRecorder is a go2rtc that remembers every PUT's src.
func putRecorder(t *testing.T) (*go2rtcmgr.APIClient, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			mu.Lock()
			puts = append(puts, r.URL.Query().Get("src"))
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop()), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), puts...)
	}
}

func TestSetQuality_SavesByMACAndRestartsOnce(t *testing.T) {
	// A quality change is saved by MAC and restarts the stream once with
	// the new subtype. The same value again doesn't restart, and "" goes
	// back to the default and forgets the saved choice.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, _ := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "pet_cam", "192.168.4.140")
	api, puts := putRecorder(t)
	mgr.SetGo2RTCAPI(api)

	var saved map[string]string
	var announced []string
	mgr.OnQualityChange(func(c *Camera, q string, s map[string]string) {
		saved = s
		announced = append(announced, c.Name()+"="+q)
	})
	var reasons []string
	mgr.OnStreamRestart(func(_ *Camera, reason string) { reasons = append(reasons, reason) })

	got, err := mgr.SetQuality(context.Background(), "pet_cam", " SD ")
	if err != nil || got != "sd" {
		t.Fatalf("SetQuality = %q, %v", got, err)
	}
	if saved["D03F27B081EF"] != "sd" {
		t.Errorf("saved = %v, want sd under the MAC", saved)
	}
	if p := puts(); len(p) != 1 || !strings.Contains(p[0], "subtype=sd") {
		t.Errorf("go2rtc puts = %v, want one with subtype=sd", p)
	}
	if len(reasons) != 1 || reasons[0] != "quality now sd" {
		t.Errorf("restart reasons = %v", reasons)
	}

	// Same value again: saved, but the stream is left alone.
	if _, err := mgr.SetQuality(context.Background(), "pet_cam", "sd"); err != nil {
		t.Fatal(err)
	}
	if len(puts()) != 1 || len(reasons) != 1 {
		t.Errorf("unchanged quality restarted the stream: puts=%v reasons=%v", puts(), reasons)
	}

	// "" goes back to the default and forgets the choice.
	got, err = mgr.SetQuality(context.Background(), "pet_cam", "")
	if err != nil || got != "hd" || cam.GetQuality() != "hd" {
		t.Fatalf("back to default: %q, %v, camera %q", got, err, cam.GetQuality())
	}
	if _, ok := saved["D03F27B081EF"]; ok {
		t.Errorf("default should drop the saved entry: %v", saved)
	}
	if mgr.SavedQuality(cam) != "" {
		t.Error("SavedQuality should be empty after going back to the default")
	}
	// Every set is announced with the quality in effect, for MQTT.
	if want := "pet_cam=sd pet_cam=sd pet_cam=hd"; strings.Join(announced, " ") != want {
		t.Errorf("announced %v, want %s", announced, want)
	}
}

func TestSetQuality_Rejects(t *testing.T) {
	// Unknown qualities and non-TUTK cameras (a doorbell) are refused,
	// and a refused change leaves the camera's quality alone.
	mgr, _ := newLanIPTestManager(t, nil)
	mgr.cameras["pet_cam"] = newV3("pet_cam", "192.168.4.140")
	door := NewCamera(wyzeapi.CameraInfo{Name: "front_door", Model: "GW_DBD", MAC: "AABBCCDDEEFF"}, "hd", true, false)
	mgr.cameras["front_door"] = door

	if _, err := mgr.SetQuality(context.Background(), "pet_cam", "4k"); !errors.Is(err, ErrBadQuality) {
		t.Errorf("4k: %v", err)
	}
	if _, err := mgr.SetQuality(context.Background(), "front_door", "sd"); !errors.Is(err, ErrQualityNotTUTK) {
		t.Errorf("doorbell: %v", err)
	}
	if door.GetQuality() != "hd" {
		t.Error("a rejected change must not touch the camera")
	}
}

func TestSavedQualities_AppliedToCamerasJoiningLater(t *testing.T) {
	// Saved qualities load normalized (bad MACs and values dropped) and
	// apply as a camera joins, so its first stream URL already carries
	// the saved subtype.
	mgr, _ := newLanIPTestManager(t, nil)
	mgr.SetSavedQualities(map[string]string{
		"d03f27b081ef": "SD",    // normalized
		"AABB":         "ultra", // dropped
		"":             "sd",    // dropped
	})
	if got := mgr.SavedQualities(); len(got) != 1 || got["D03F27B081EF"] != "sd" {
		t.Fatalf("saved = %v", got)
	}
	cam := newV3("pet_cam", "192.168.4.140")
	mgr.mu.Lock()
	mgr.applyQualityLocked(cam)
	mgr.cameras["pet_cam"] = cam
	mgr.mu.Unlock()
	if cam.GetQuality() != "sd" || !strings.Contains(cam.StreamURL(), "subtype=sd") {
		t.Errorf("camera joined with %q (%s)", cam.GetQuality(), cam.StreamURL())
	}

	rows := mgr.QualityRows()
	if len(rows) != 1 || rows[0].Saved != "sd" || rows[0].Default != "hd" || !rows[0].TUTK {
		t.Errorf("rows = %+v", rows)
	}
}
