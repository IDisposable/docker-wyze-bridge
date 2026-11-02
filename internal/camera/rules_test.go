package camera

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func TestGlobMatch(t *testing.T) {
	// Name rules match case-insensitively with * as the only wildcard.
	// ? and brackets are literal, so odd nicknames still match themselves.
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"pet cam", "Pet Cam", true},
		{"pet cam", "Pet Cam2", false},
		{"pet cam*", "Pet Cam2", true},
		{"pet*", "pet", true},
		{"*cam", "Front Light Cam", true},
		{"*cam", "Front Light Cams", false},
		{"*light*", "Front Light Cam", true},
		{"front*cam", "Front Door Cam", true},
		{"front*door*cam", "Front Door Cam", true},
		{"front*door*cam", "Front Cam Door", false},
		{"*", "anything", true},
		{"*", "", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "ac", false},
		{"what?", "what?", true}, // ? is literal
		{"[x]", "[x]", true},     // so are brackets
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestCompileRules(t *testing.T) {
	// Rules from the Settings page are trimmed and normalized (subnets to
	// the network address, MACs and models uppercased, blanks dropped),
	// and a bad scope or a malformed or IPv6 subnet is refused.
	st, err := compileRules(wyzeapi.CameraRules{
		Scope:   " Local ",
		Subnets: []string{" 192.168.4.17/22 ", "", "10.0.0.0/8"},
		Names:   []string{"Pet Cam*", " ", "garage"},
		MACs:    []string{"d0:3f:27:00:ca:04", "aabb01", ""},
		Models:  []string{"hl_cam4", "V3"},
		Block:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.local || st.raw.Scope != "local" {
		t.Errorf("scope: %+v", st.raw)
	}
	if strings.Join(st.raw.Subnets, ",") != "192.168.4.0/22,10.0.0.0/8" {
		t.Errorf("subnets normalized to the network address: %v", st.raw.Subnets)
	}
	if strings.Join(st.raw.Names, ",") != "Pet Cam*,garage" || strings.Join(st.names, ",") != "pet cam*,garage" {
		t.Errorf("names: %v / %v", st.raw.Names, st.names)
	}
	if !st.macs["D0:3F:27:00:CA:04"] || !st.macs["AABB01"] || len(st.macs) != 2 {
		t.Errorf("macs: %v", st.macs)
	}
	if !st.models["HL_CAM4"] || !st.models["V3"] {
		t.Errorf("models: %v", st.models)
	}
	if !st.raw.Block {
		t.Error("block flag lost")
	}

	for _, bad := range []wyzeapi.CameraRules{
		{Scope: "lan"},
		{Subnets: []string{"192.168.4.0"}},
		{Subnets: []string{"fd00::/64"}},
		{Subnets: []string{"192.168.4.0/33"}},
	} {
		if _, err := compileRules(bad); err == nil {
			t.Errorf("compileRules(%+v) should fail", bad)
		}
	}
	if st, err := compileRules(wyzeapi.CameraRules{Scope: "any"}); err != nil || st.raw.Scope != "" || !st.raw.Empty() {
		t.Errorf("any: %+v %v", st, err)
	}
}

func TestReasonToDrop(t *testing.T) {
	// Every drop carries the reason the dashboard shows. Local scope only
	// drops cameras whose address is off the known subnets; the block and
	// allow lists run after it, and the scope verdict is reported first.
	_, lan, _ := net.ParseCIDR("192.168.4.0/22")
	v3 := func(nick, mac, ip string) wyzeapi.CameraInfo {
		return wyzeapi.CameraInfo{Nickname: nick, MAC: mac, Model: "WYZE_CAKP2JFUS", LanIP: ip}
	}
	must := func(r wyzeapi.CameraRules) *rulesState {
		st, err := compileRules(r)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	// Scope local: off-subnet cameras go, on-subnet and addressless stay.
	local := must(wyzeapi.CameraRules{Scope: "local"})
	if got := local.reasonToDrop(v3("Side Garage", "A1", "192.168.4.151"), []*net.IPNet{lan}); got != "" {
		t.Errorf("on-LAN camera dropped: %q", got)
	}
	if got := local.reasonToDrop(v3("Mom's Porch", "A2", "192.168.1.20"), []*net.IPNet{lan}); got != "off-LAN: 192.168.1.20 isn't in 192.168.4.0/22" {
		t.Errorf("off-LAN reason = %q", got)
	}
	if got := local.reasonToDrop(v3("Unplugged", "A3", ""), []*net.IPNet{lan}); got != "" {
		t.Errorf("addressless camera must be kept: %q", got)
	}
	// No subnets known at all: the scope can't be judged, so nothing is
	// dropped for its address (applyRules warns instead).
	if got := local.reasonToDrop(v3("Mom's Porch", "A2", "192.168.1.20"), nil); got != "" {
		t.Errorf("with no local subnets the scope check must be skipped, got %q", got)
	}
	// Any scope with no lists: nothing is ever dropped.
	if got := must(wyzeapi.CameraRules{}).reasonToDrop(v3("x", "A4", "1.2.3.4"), nil); got != "" {
		t.Errorf("empty rules dropped something: %q", got)
	}

	// Block lists.
	block := must(wyzeapi.CameraRules{Names: []string{"Pet Cam*"}, MACs: []string{"AABB02"}, Models: []string{"V4"}, Block: true})
	cases := map[string]string{
		"Pet Cam2|P1|WYZE_CAKP2JFUS":     `blocked by name "pet cam*" rule`,
		"Driveway|AABB02|WYZE_CAKP2JFUS": "blocked by MAC AABB02 rule",
		"Yard|P3|HL_CAM4":                "blocked by model V4 rule",
		"Garage|P4|WYZE_CAKP2JFUS":       "",
	}
	for key, want := range cases {
		parts := strings.Split(key, "|")
		cam := wyzeapi.CameraInfo{Nickname: parts[0], MAC: parts[1], Model: parts[2]}
		if got := block.reasonToDrop(cam, nil); got != want {
			t.Errorf("block %s: got %q, want %q", key, got, want)
		}
	}

	// Allow list: only the listed survive.
	allow := must(wyzeapi.CameraRules{Names: []string{"*garage*"}})
	if got := allow.reasonToDrop(v3("Side Garage", "A1", ""), nil); got != "" {
		t.Errorf("allowed camera dropped: %q", got)
	}
	if got := allow.reasonToDrop(v3("Driveway", "A2", ""), nil); got != "not on the allow list" {
		t.Errorf("allow reason = %q", got)
	}

	// Scope and lists stack: the scope verdict comes first.
	both := must(wyzeapi.CameraRules{Scope: "local", Names: []string{"Driveway"}, Block: true})
	if got := both.reasonToDrop(v3("Driveway", "A2", "10.9.9.9"), []*net.IPNet{lan}); !strings.HasPrefix(got, "off-LAN") {
		t.Errorf("stacked: %q", got)
	}
	if got := both.reasonToDrop(v3("Driveway", "A2", "192.168.4.9"), []*net.IPNet{lan}); !strings.HasPrefix(got, "blocked by name") {
		t.Errorf("stacked on-LAN: %q", got)
	}
}

func TestDiscover_RulesDropAndRestore(t *testing.T) {
	// The mock cloud has Front Door (HL_CAM4, AABB01, 10.0.0.1) and
	// Backyard (V3, AABB02, 10.0.0.2). Both get in, then a rule drops
	// one like an ignore would, then lifting the rule brings it back.
	mgr, deleted := discoveringManager(t)
	ctx := context.Background()
	var removed, added []string
	mgr.OnCameraRemoved(func(c *Camera, reason string) { removed = append(removed, c.Name()+":"+reason) })
	mgr.OnCameraAdded(func(c *Camera) { added = append(added, c.Name()) })
	var persisted []wyzeapi.CameraRules
	mgr.OnRulesChange(func(r wyzeapi.CameraRules) { persisted = append(persisted, r) })

	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Cameras()) != 2 || len(mgr.Excluded()) != 0 {
		t.Fatalf("setup: %d cameras, excluded %v", len(mgr.Cameras()), mgr.Excluded())
	}
	cam := mgr.GetCamera("backyard")
	cam.SetState(StateStreaming)

	if err := mgr.SetRules(wyzeapi.CameraRules{Names: []string{"back*"}, Block: true}); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].Names[0] != "back*" || !persisted[0].Block {
		t.Errorf("persist hook got %+v", persisted)
	}
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if mgr.GetCamera("backyard") != nil || mgr.GetCamera("front_door") == nil {
		t.Error("rules should have dropped backyard and kept front_door")
	}
	if cam.GetState() != StateOffline {
		t.Errorf("dropped camera state = %v, want offline", cam.GetState())
	}
	if d := deleted(); len(d) != 1 || d[0] != "backyard" {
		t.Errorf("go2rtc deletes = %v", d)
	}
	if len(removed) != 1 || removed[0] != "backyard:excluded" {
		t.Errorf("removed = %v", removed)
	}
	ex := mgr.Excluded()
	if len(ex) != 1 || ex[0].MAC != "AABB02" || ex[0].Nickname != "Backyard" || ex[0].IP != "10.0.0.2" || ex[0].Reason != `blocked by name "back*" rule` {
		t.Errorf("excluded = %+v", ex)
	}
	// A second discovery with the same rules is quiet.
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || len(deleted()) != 1 {
		t.Errorf("repeat discovery should not re-drop: removed=%v deletes=%v", removed, deleted())
	}

	// Lift the rule: the camera comes back through the normal added path.
	added = nil
	if err := mgr.SetRules(wyzeapi.CameraRules{}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if mgr.GetCamera("backyard") == nil || len(added) != 1 || added[0] != "backyard" || len(mgr.Excluded()) != 0 {
		t.Errorf("restore: added=%v excluded=%v", added, mgr.Excluded())
	}

	// Invalid rules are refused and leave the old ones in place.
	if err := mgr.SetRules(wyzeapi.CameraRules{Scope: "nope"}); err == nil {
		t.Error("bad scope accepted")
	}
	if len(persisted) != 2 {
		t.Errorf("refused rules must not persist: %d", len(persisted))
	}
}

func TestDiscover_LocalScopeUsesHostSubnets(t *testing.T) {
	// Local scope checks cameras against the host's subnets plus any
	// extra CIDRs. With no subnets known nothing is dropped, and narrowing
	// the LAN drops the camera that falls outside it.
	mgr, _ := discoveringManager(t)
	ctx := context.Background()

	// Scope local with no way to learn the subnets and none listed:
	// both cameras stay, since the rule can't be judged.
	if err := mgr.SetRules(wyzeapi.CameraRules{Scope: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Cameras()) != 2 || len(mgr.Excluded()) != 0 {
		t.Fatalf("no subnets known: cameras=%d excluded=%v", len(mgr.Cameras()), mgr.Excluded())
	}

	_, lan, _ := net.ParseCIDR("10.0.0.0/24")
	mgr.SetLocalSubnets(func() ([]*net.IPNet, error) { return []*net.IPNet{lan}, nil })
	if err := mgr.SetRules(wyzeapi.CameraRules{Scope: "local", Subnets: []string{"172.16.0.0/12"}}); err != nil {
		t.Fatal(err)
	}
	if got := mgr.LocalSubnets(); len(got) != 2 || got[0].String() != "10.0.0.0/24" || got[1].String() != "172.16.0.0/12" {
		t.Errorf("LocalSubnets = %v", got)
	}
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mgr.Cameras()) != 2 || len(mgr.Excluded()) != 0 {
		t.Errorf("both mock cameras are on 10.0.0.0/24: cameras=%d excluded=%v", len(mgr.Cameras()), mgr.Excluded())
	}
	// Narrow the LAN so .2 falls outside it.
	_, lan2, _ := net.ParseCIDR("10.0.0.0/31")
	mgr.SetLocalSubnets(func() ([]*net.IPNet, error) { return []*net.IPNet{lan2}, nil })
	if err := mgr.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	ex := mgr.Excluded()
	if mgr.GetCamera("backyard") != nil || len(ex) != 1 || ex[0].Reason != "off-LAN: 10.0.0.2 isn't in 10.0.0.0/31, 172.16.0.0/12" {
		t.Errorf("excluded = %+v", ex)
	}
	if r := mgr.Rules(); r.Scope != "local" || len(r.Subnets) != 1 {
		t.Errorf("Rules() = %+v", r)
	}
}
