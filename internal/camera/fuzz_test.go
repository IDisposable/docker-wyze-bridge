package camera

import (
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Rules come from a text box on the Settings page, so the matcher and
// the compiler get arbitrary strings. Neither may panic, and a rule
// that compiles must compile to the same thing twice (the page echoes
// the normalized form back and the next save sends it again).

func FuzzGlobMatch(f *testing.F) {
	f.Add("pet cam*", "Pet Cam2")
	f.Add("*", "")
	f.Add("", "")
	f.Add("a*b*c", "abc")
	f.Add("**", "x")
	f.Add("*cam", "cam")
	f.Fuzz(func(t *testing.T, pattern, s string) {
		got := globMatch(pattern, s)
		lp, ls := strings.ToLower(pattern), strings.ToLower(s)
		if !strings.Contains(pattern, "*") && got != (lp == ls) {
			t.Fatalf("no wildcard: globMatch(%q, %q) = %v", pattern, s, got)
		}
		if pattern == "*" && !got {
			t.Fatalf("* must match %q", s)
		}
		if lp == ls && !got {
			t.Fatalf("a pattern must match itself: %q", pattern)
		}
	})
}

func FuzzCompileRules(f *testing.F) {
	// Whatever the Settings box sends either fails to compile or compiles
	// to a form that recompiles to itself, and evaluating the result
	// against any camera never panics.
	f.Add("local", "192.168.4.0/22", "Pet Cam*", "D03F2700CA04", "V3", true)
	f.Add("any", "", "", "", "", false)
	f.Add("", "10.0.0.5/8", "*", "d0:3f:27:00:ca:04", "wyze_cakp2jfus", false)
	f.Add("LAN", "fd00::/64", " ", "", "", true)
	f.Fuzz(func(t *testing.T, scope, subnet, name, mac, model string, block bool) {
		in := wyzeapi.CameraRules{Scope: scope, Subnets: []string{subnet}, Names: []string{name}, MACs: []string{mac}, Models: []string{model}, Block: block}
		st, err := compileRules(in)
		if err != nil {
			return
		}
		again, err := compileRules(st.raw)
		if err != nil {
			t.Fatalf("normalized rules don't recompile: %+v: %v", st.raw, err)
		}
		if strings.Join(again.raw.Subnets, ",") != strings.Join(st.raw.Subnets, ",") ||
			strings.Join(again.raw.Names, ",") != strings.Join(st.raw.Names, ",") ||
			strings.Join(again.raw.MACs, ",") != strings.Join(st.raw.MACs, ",") ||
			strings.Join(again.raw.Models, ",") != strings.Join(st.raw.Models, ",") ||
			again.raw.Scope != st.raw.Scope || again.raw.Block != st.raw.Block {
			t.Fatalf("normalization not idempotent: %+v vs %+v", st.raw, again.raw)
		}
		// Evaluating never panics either, whatever the camera looks like.
		cam := wyzeapi.CameraInfo{Nickname: name, MAC: mac, Model: model, LanIP: subnet}
		_ = st.reasonToDrop(cam, nil)
		_ = st.reasonToDrop(cam, st.subnets)
	})
}
