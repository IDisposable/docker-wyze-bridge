package camera

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/lanresolve"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestCamera_PinnedIP_BeatsResolverAndCloud(t *testing.T) {
	// An operator's pinned address wins over both the resolver and the
	// cloud and survives a cloud refresh. Unpinning falls back to the
	// resolver's address, not the cloud's.
	cam := newV3("side_garage", "192.168.4.123")
	cam.SetLanIP("192.168.4.151")
	if !cam.SetPinnedIP("192.168.4.200") {
		t.Fatal("pinning a new address should report a change")
	}
	if got := cam.GetInfo().LanIP; got != "192.168.4.200" {
		t.Errorf("GetInfo = %s, want the pin", got)
	}
	if !strings.HasPrefix(cam.StreamURL(), "wyze://192.168.4.200?") {
		t.Errorf("StreamURL = %s", cam.StreamURL())
	}
	if cam.StatusJSON()["pinned_ip"] != "192.168.4.200" || cam.StatusJSON()["ip"] != "192.168.4.200" || cam.StatusJSON()["cloud_ip"] != "192.168.4.123" {
		t.Errorf("StatusJSON = %v", cam.StatusJSON())
	}
	if cam.Snapshot().PinnedIP != "192.168.4.200" {
		t.Error("snapshot lacks the pin")
	}
	// A refresh from the cloud can't dislodge it.
	cam.UpdateInfo(wyzeapi.CameraInfo{Name: "side_garage", MAC: "D03F27B081EF", LanIP: "192.168.4.99"})
	if got := cam.GetInfo().LanIP; got != "192.168.4.200" {
		t.Errorf("after UpdateInfo = %s", got)
	}
	// Same pin again: no change. Unpin: back to the resolver's address,
	// not the cloud's.
	if cam.SetPinnedIP("192.168.4.200") {
		t.Error("re-pinning the same address is not a change")
	}
	if !cam.SetPinnedIP("") || cam.GetInfo().LanIP != "192.168.4.151" || cam.PinnedIP() != "" {
		t.Errorf("after unpin: ip=%s pin=%q", cam.GetInfo().LanIP, cam.PinnedIP())
	}
}

func TestPinLanIP_ValidatesAndPersists(t *testing.T) {
	// Bad addresses and unknown cameras are refused with nothing saved.
	// A good pin is saved by MAC, re-registers the stream and keeps the
	// recheck and resolver ladder away; unpinning undoes all of it.
	r := &scriptedResolver{result: lanresolve.Result{Verified: true, Source: "cloud"}, checkOK: true}
	mgr, srcOf := newLanIPTestManager(t, r)
	cam := streamingV3(t, mgr, "side_garage", "192.168.4.123")
	var saved []map[string]string
	mgr.OnPinChange(func(pins map[string]string) { saved = append(saved, pins) })
	var changes []string
	mgr.OnLanIPChange(func(c *Camera, oldIP, newIP, source string) {
		changes = append(changes, oldIP+">"+newIP+":"+source)
	})

	for _, bad := range []string{"garage", "192.168.4", "fe80::1", "192.168.4.300"} {
		if _, err := mgr.PinLanIP(context.Background(), "side_garage", bad); !errors.Is(err, ErrBadPinIP) {
			t.Errorf("PinLanIP(%q) err = %v, want ErrBadPinIP", bad, err)
		}
	}
	if _, err := mgr.PinLanIP(context.Background(), "nope", "192.168.4.1"); !errors.Is(err, ErrUnknownCamera) {
		t.Errorf("unknown camera err = %v", err)
	}
	if len(saved) != 0 || len(changes) != 0 {
		t.Fatal("rejected pins must not persist or fire")
	}

	// A good pin: effective at once, persisted by MAC, announced like a
	// resolver correction, and the go2rtc stream re-registered.
	got, err := mgr.PinLanIP(context.Background(), "side_garage", " 192.168.4.200 ")
	if err != nil || got != "192.168.4.200" {
		t.Fatalf("PinLanIP = %q, %v", got, err)
	}
	if len(saved) != 1 || saved[0]["D03F27B081EF"] != "192.168.4.200" {
		t.Errorf("persist hook got %v", saved)
	}
	if len(changes) != 1 || changes[0] != "192.168.4.123>192.168.4.200:pinned" {
		t.Errorf("changes = %v", changes)
	}
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.200?") {
		t.Errorf("go2rtc should dial the pin, has %q", src)
	}
	if cam.GetState() != StateStreaming {
		t.Errorf("state after re-register = %v", cam.GetState())
	}

	// The periodic recheck leaves pinned cameras alone, and a reconnect
	// doesn't run the resolver ladder, just a status probe.
	checks := len(r.checkCalls())
	mgr.RecheckLanIPs(context.Background())
	if len(r.checkCalls()) != checks {
		t.Error("recheck must skip pinned cameras")
	}
	resolves := len(r.resolveCalls())
	mgr.RestartStream(context.Background(), "side_garage")
	if len(r.resolveCalls()) != resolves {
		t.Error("a pinned camera must not run the resolver ladder")
	}
	if src, _, _ := cam.LanStatus(); src != "pinned" {
		t.Errorf("status source = %q, want pinned (the probe answered)", src)
	}
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.200?") {
		t.Errorf("reconnect should keep the pin, has %q", src)
	}

	// Unpin: back to the cloud address (nothing resolved underneath),
	// persisted as an empty map, stream re-registered.
	got, err = mgr.PinLanIP(context.Background(), "side_garage", "")
	if err != nil || got != "192.168.4.123" {
		t.Fatalf("unpin = %q, %v", got, err)
	}
	if len(saved) != 2 || len(saved[1]) != 0 {
		t.Errorf("persist hook after unpin got %v", saved)
	}
	if len(changes) != 2 || changes[1] != "192.168.4.200>192.168.4.123:unpinned" {
		t.Errorf("changes = %v", changes)
	}
	if src := srcOf("side_garage"); !strings.HasPrefix(src, "wyze://192.168.4.123?") {
		t.Errorf("go2rtc after unpin has %q", src)
	}
}

func TestPinLanIP_OnlyForLANCameras(t *testing.T) {
	// A pin only means something for cameras go2rtc dials on the LAN, so
	// a WebRTC doorbell is refused and keeps no pin.
	mgr, _ := newLanIPTestManager(t, nil)
	doorbell := NewCamera(wyzeapi.CameraInfo{Name: "front_door", Model: "GW_BE1", MAC: "GW_BE1_80482C0D6B01", LanIP: "192.168.4.9"}, "hd", true, false)
	mgr.cameras["front_door"] = doorbell
	if _, err := mgr.PinLanIP(context.Background(), "front_door", "192.168.4.10"); !errors.Is(err, ErrNotLANCamera) {
		t.Errorf("doorbell pin err = %v, want ErrNotLANCamera", err)
	}
	if doorbell.PinnedIP() != "" {
		t.Error("a refused pin must not stick")
	}
}

func TestSetPinnedIPs_AppliesAtStartupAndOnDiscovery(t *testing.T) {
	// Pins loaded from the state file apply to cameras already known and
	// to ones discovered later. MACs match in any case, and entries with
	// a bad address or no MAC are dropped.
	mgr, _ := newLanIPTestManager(t, nil)
	existing := newV3("side_garage", "192.168.4.123")
	mgr.cameras["side_garage"] = existing
	mgr.SetPinnedIPs(map[string]string{
		"d03f27b081ef": "192.168.4.200", // lowercase in the file still matches
		"AABBCCDDEEFF": "192.168.4.201", // not discovered yet
		"BAD":          "not-an-ip",     // ignored
		"":             "192.168.4.5",
	})
	if existing.GetInfo().LanIP != "192.168.4.200" {
		t.Errorf("existing camera didn't pick up its pin: %s", existing.GetInfo().LanIP)
	}
	pins := mgr.PinnedIPs()
	if len(pins) != 2 || pins["D03F27B081EF"] != "192.168.4.200" || pins["AABBCCDDEEFF"] != "192.168.4.201" {
		t.Errorf("pins = %v", pins)
	}
	// A camera arriving later gets its pin on the way in.
	mgr.mu.Lock()
	late := NewCamera(wyzeapi.CameraInfo{Name: "attic", Model: "WYZE_CAKP2JFUS", MAC: "AABBCCDDEEFF", LanIP: "192.168.4.77"}, "hd", true, false)
	mgr.applyPinLocked(late)
	mgr.mu.Unlock()
	if late.GetInfo().LanIP != "192.168.4.201" {
		t.Errorf("late camera ip = %s", late.GetInfo().LanIP)
	}
}
