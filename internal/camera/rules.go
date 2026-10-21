package camera

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Runtime camera rules, edited on the WebUI's Settings page and kept
// in the state file. They run after the env filters (FILTER_*) and the
// ignore list on every Discover, so nothing anyone configured through
// the environment changes; they just add a layer that can be edited
// without a redeploy.
//
// Two kinds of rule. A network scope: "local" keeps only cameras whose
// cloud-reported address sits on one of the bridge's own subnets (or
// on an extra CIDR the operator lists), which is the cheap way to drop
// the cameras a shared Wyze account sees at other houses. And lists by
// nickname pattern, MAC and model, in block or allow mode like the env
// filters. A camera the rules drop is removed from the registry the
// same way an ignored one is (go2rtc stream deleted, card and HA
// entities retracted) and listed, with the reason, so the dashboard
// explains itself.

// Excluded is a camera the rules dropped at the last discovery.
type Excluded struct {
	MAC      string `json:"mac"`
	Nickname string `json:"nickname"`
	Model    string `json:"model"`
	IP       string `json:"ip,omitempty"`
	Reason   string `json:"reason"`
}

// rulesState is the compiled form of wyzeapi.CameraRules: parsed CIDRs
// and normalized lists, so Discover doesn't reparse on every pass.
type rulesState struct {
	raw     wyzeapi.CameraRules
	local   bool
	subnets []*net.IPNet
	names   []string // lowercased patterns
	macs    map[string]bool
	models  map[string]bool // uppercased codes and display names
}

// compileRules validates and normalizes rules. It's what both SetRules
// and the WebUI's PUT handler use, so a rule that saves is a rule that
// applies.
func compileRules(r wyzeapi.CameraRules) (*rulesState, error) {
	st := &rulesState{macs: map[string]bool{}, models: map[string]bool{}}
	switch strings.ToLower(strings.TrimSpace(r.Scope)) {
	case "", "any":
		st.raw.Scope = ""
	case "local":
		st.raw.Scope = "local"
		st.local = true
	default:
		return nil, fmt.Errorf("scope must be \"any\" or \"local\", not %q", r.Scope)
	}
	for _, s := range r.Subnets {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, ipn, err := net.ParseCIDR(s)
		if err != nil || ipn.IP.To4() == nil {
			return nil, fmt.Errorf("subnet %q is not an IPv4 CIDR like 192.168.4.0/22", s)
		}
		st.subnets = append(st.subnets, ipn)
		st.raw.Subnets = append(st.raw.Subnets, ipn.String())
	}
	for _, n := range r.Names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		st.names = append(st.names, strings.ToLower(n))
		st.raw.Names = append(st.raw.Names, n)
	}
	for _, m := range r.MACs {
		m = normalizeIgnoreMAC(m)
		if m == "" {
			continue
		}
		st.macs[m] = true
		st.raw.MACs = append(st.raw.MACs, m)
	}
	for _, m := range r.Models {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		st.models[m] = true
		st.raw.Models = append(st.raw.Models, m)
	}
	st.raw.Block = r.Block
	return st, nil
}

// hasLists reports whether any name / MAC / model rule is set; without
// one, allow mode means nothing and every camera passes that step.
func (st *rulesState) hasLists() bool {
	return len(st.names) > 0 || len(st.macs) > 0 || len(st.models) > 0
}

// listMatch returns the rule a camera matches, "" if none.
func (st *rulesState) listMatch(cam wyzeapi.CameraInfo) string {
	nick := strings.ToLower(strings.TrimSpace(cam.Nickname))
	for _, pat := range st.names {
		if globMatch(pat, nick) {
			return "name " + strconvQuote(pat)
		}
	}
	if mac := normalizeIgnoreMAC(cam.MAC); mac != "" && st.macs[mac] {
		return "MAC " + mac
	}
	if st.models[strings.ToUpper(cam.Model)] || st.models[strings.ToUpper(cam.ModelName())] {
		return "model " + cam.ModelName()
	}
	return ""
}

func strconvQuote(s string) string { return "\"" + s + "\"" }

// globMatch is case-insensitive wildcard matching where * stands for
// any run of characters (including none). Nothing else is special, so
// a nickname with brackets or question marks matches literally.
func globMatch(pattern, s string) bool {
	pattern, s = strings.ToLower(pattern), strings.ToLower(s)
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(s, parts[i])
		if idx < 0 {
			return false
		}
		s = s[idx+len(parts[i]):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// SetRules installs rules (startup, or a Settings save). The new rules
// take effect on the next Discover; callers that want them applied
// right away run one. Invalid rules are refused and nothing changes.
func (m *Manager) SetRules(r wyzeapi.CameraRules) error {
	st, err := compileRules(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.rules = st
	m.mu.Unlock()
	if m.onRulesChange != nil {
		m.onRulesChange(st.raw)
	}
	return nil
}

// Rules returns the normalized rules in effect.
func (m *Manager) Rules() wyzeapi.CameraRules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.rules == nil {
		return wyzeapi.CameraRules{}
	}
	r := m.rules.raw
	r.Subnets = append([]string(nil), r.Subnets...)
	r.Names = append([]string(nil), r.Names...)
	r.MACs = append([]string(nil), r.MACs...)
	r.Models = append([]string(nil), r.Models...)
	return r
}

// OnRulesChange registers the persistence hook: fires with the
// normalized rules after every accepted SetRules.
func (m *Manager) OnRulesChange(fn func(wyzeapi.CameraRules)) {
	m.onRulesChange = fn
}

// SetLocalSubnets tells the manager how to learn the bridge's own
// subnets for the "local" scope (lanresolve.LocalSubnets in
// production). Without it only the rules' extra CIDRs count as local.
func (m *Manager) SetLocalSubnets(fn func() ([]*net.IPNet, error)) {
	m.mu.Lock()
	m.localSubnets = fn
	m.mu.Unlock()
}

// LocalSubnets returns the subnets the "local" scope currently
// recognizes: the host's, then the rules' extra CIDRs. For the
// Settings page.
func (m *Manager) LocalSubnets() []*net.IPNet {
	m.mu.RLock()
	fn, st := m.localSubnets, m.rules
	m.mu.RUnlock()
	var out []*net.IPNet
	if fn != nil {
		if nets, err := fn(); err == nil {
			out = append(out, nets...)
		}
	}
	if st != nil {
		out = append(out, st.subnets...)
	}
	return out
}

// Excluded lists the cameras the rules dropped at the last discovery,
// sorted by nickname.
func (m *Manager) Excluded() []Excluded {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]Excluded(nil), m.excluded...)
	sort.Slice(out, func(i, j int) bool { return out[i].Nickname < out[j].Nickname })
	return out
}

// applyRules splits discovered cameras into the ones the rules keep and
// the ones they drop, with a reason per drop. Runs every Discover; the
// subnet list is fetched once per call.
func (m *Manager) applyRules(cameras []wyzeapi.CameraInfo) ([]wyzeapi.CameraInfo, []Excluded) {
	m.mu.RLock()
	st := m.rules
	m.mu.RUnlock()
	if st == nil || (st.raw.Empty() && len(st.subnets) == 0) {
		return cameras, nil
	}
	var local []*net.IPNet
	if st.local {
		local = m.LocalSubnets()
		if len(local) == 0 {
			// Can't tell what's local, so don't guess: the scope check is
			// skipped (nothing is dropped for its address) until a
			// subnet is known, and the log says why. The Settings page
			// shows the same thing next to "Detected on this host".
			m.log.Warn().Msg("camera rules: scope is \"local\" but no local subnets are known (no LAN interface and none listed), so no camera is excluded by address")
		}
	}
	kept := make([]wyzeapi.CameraInfo, 0, len(cameras))
	var dropped []Excluded
	for _, cam := range cameras {
		if reason := st.reasonToDrop(cam, local); reason != "" {
			dropped = append(dropped, Excluded{
				MAC:      normalizeIgnoreMAC(cam.MAC),
				Nickname: cam.Nickname,
				Model:    cam.Model,
				IP:       cam.LanIP,
				Reason:   reason,
			})
			m.log.Debug().Str("cam", cam.Nickname).Str("reason", reason).Msg("camera excluded by rules")
			continue
		}
		kept = append(kept, cam)
	}
	return kept, dropped
}

// reasonToDrop evaluates one camera: scope first, then the lists.
// A camera with no address can't fail the scope check — an offline
// camera at this house shouldn't vanish from the dashboard — so it's
// kept and left to the lists. With no local subnets known at all the
// scope check is skipped entirely (see applyRules).
func (st *rulesState) reasonToDrop(cam wyzeapi.CameraInfo, local []*net.IPNet) string {
	if st.local && len(local) > 0 {
		if ip := net.ParseIP(cam.LanIP); ip != nil && ip.To4() != nil && !ip.IsUnspecified() {
			onLAN := false
			for _, n := range local {
				if n.Contains(ip) {
					onLAN = true
					break
				}
			}
			if !onLAN {
				return "off-LAN: " + cam.LanIP + " isn't in " + joinNets(local)
			}
		}
	}
	if !st.hasLists() {
		return ""
	}
	match := st.listMatch(cam)
	switch {
	case st.raw.Block && match != "":
		return "blocked by " + match + " rule"
	case !st.raw.Block && match == "":
		return "not on the allow list"
	}
	return ""
}

func joinNets(nets []*net.IPNet) string {
	parts := make([]string, len(nets))
	for i, n := range nets {
		parts[i] = n.String()
	}
	return strings.Join(parts, ", ")
}

// dropExcludedLocked removes cameras already in the registry that this
// discovery's rules excluded: the go2rtc stream goes, the camera is
// marked offline so recording, MQTT and webhooks see a normal shutdown,
// and it leaves the registry. Returns them so Discover can fire
// onRemoved once the lock is released. Call with m.mu held.
func (m *Manager) dropExcludedLocked(ctx context.Context, dropped []Excluded) []*Camera {
	if len(dropped) == 0 {
		return nil
	}
	byMAC := make(map[string]bool, len(dropped))
	for _, d := range dropped {
		byMAC[d.MAC] = true
	}
	var removed []*Camera
	for name, cam := range m.cameras {
		if !byMAC[normalizeIgnoreMAC(cam.Info.MAC)] {
			continue
		}
		if go2rtc := m.go2rtcClient(); go2rtc != nil {
			_ = go2rtc.DeleteStream(ctx, name)
		}
		m.changeState(cam, StateOffline)
		delete(m.cameras, name)
		removed = append(removed, cam)
		m.log.Info().Str("cam", name).Msg("camera dropped by rules")
	}
	return removed
}
