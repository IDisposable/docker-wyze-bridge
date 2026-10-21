package webui

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// The Settings page edits the runtime camera rules (network scope and
// name / MAC / model lists; see camera/rules.go). The manager owns and
// applies them; this file is the page, GET/PUT /api/settings and
// GET /api/excluded. A save that validates is applied on the spot by
// kicking a discovery, the same one the Rediscover button runs.

// maxRuleEntries bounds each list in a PUT /api/settings body.
const maxRuleEntries = 256

// handleAPISettings serves GET /api/settings (the rules in effect) and
// PUT or POST /api/settings (replace them). The body is a
// wyzeapi.CameraRules; the manager normalizes and validates it, and a
// bad body changes nothing.
func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, settingsJSON(s.camMgr.Rules()))
	case http.MethodPut, http.MethodPost:
		var body wyzeapi.CameraRules
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		for _, list := range [][]string{body.Subnets, body.Names, body.MACs, body.Models} {
			if len(list) > maxRuleEntries {
				writeJSONError(w, http.StatusBadRequest, "too many entries")
				return
			}
		}
		if err := s.camMgr.SetRules(body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if evLog := s.Events(); evLog != nil {
			evLog.Record(Event{Kind: "rules", Message: "camera rules saved: " + describeRules(s.camMgr.Rules())})
		}
		// Apply now rather than at the next scheduled discovery.
		if s.onDiscoverReq != nil {
			go s.onDiscoverReq(s.rootCtx)
		}
		writeJSON(w, settingsJSON(s.camMgr.Rules()))
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or PUT required")
	}
}

// settingsJSON is the rules with lists never null, so the page's JS can
// treat every field as an array.
func settingsJSON(r wyzeapi.CameraRules) map[string]interface{} {
	nonNil := func(l []string) []string {
		if l == nil {
			return []string{}
		}
		return l
	}
	scope := r.Scope
	if scope == "" {
		scope = "any"
	}
	return map[string]interface{}{
		"scope":   scope,
		"subnets": nonNil(r.Subnets),
		"names":   nonNil(r.Names),
		"macs":    nonNil(r.MACs),
		"models":  nonNil(r.Models),
		"block":   r.Block,
	}
}

// describeRules is the one-line event log summary of a save.
func describeRules(r wyzeapi.CameraRules) string {
	if r.Empty() {
		return "none"
	}
	var parts []string
	if r.Scope == "local" {
		parts = append(parts, "local subnets only")
	}
	mode := "allow"
	if r.Block {
		mode = "block"
	}
	if n := len(r.Names) + len(r.MACs) + len(r.Models); n > 0 {
		parts = append(parts, mode+" list of "+strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}

// handleAPIExcluded serves GET /api/excluded: the cameras the rules
// dropped at the last discovery, with the reason for each.
func (s *Server) handleAPIExcluded(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	list := s.camMgr.Excluded()
	if list == nil {
		list = []camera.Excluded{}
	}
	writeJSON(w, list)
}

// handleSettingsPage renders /settings.
func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/settings" {
		http.NotFound(w, r)
		return
	}
	rules := s.camMgr.Rules()
	var detected []string
	for _, n := range s.camMgr.LocalSubnets() {
		detected = append(detected, n.String())
	}
	env := s.envFilterSummary()
	data := map[string]interface{}{
		"Version":    s.version,
		"BasePath":   ingressBasePath(r),
		"Scope":      settingsJSON(rules)["scope"],
		"Subnets":    strings.Join(rules.Subnets, "\n"),
		"Names":      strings.Join(rules.Names, "\n"),
		"MACs":       strings.Join(rules.MACs, "\n"),
		"Models":     strings.Join(rules.Models, "\n"),
		"Block":      rules.Block,
		"Detected":   strings.Join(detected, ", "),
		"EnvFilters": env,
		"Excluded":   excludedRows(s.camMgr.Excluded()),
	}
	tmpl, err := template.New("settings").Funcs(templateFuncs).Parse(settingsHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		s.log.Error().Err(err).Msg("settings template parse error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		s.log.Error().Err(err).Msg("settings template execute error")
	}
}

// envFilterSummary says what FILTER_* is doing, since those run before
// the rules and a camera they drop never reaches this page.
func (s *Server) envFilterSummary() string {
	var parts []string
	if len(s.cfg.FilterNames) > 0 {
		parts = append(parts, "FILTER_NAMES="+strings.Join(s.cfg.FilterNames, ","))
	}
	if len(s.cfg.FilterModels) > 0 {
		parts = append(parts, "FILTER_MODELS="+strings.Join(s.cfg.FilterModels, ","))
	}
	if len(s.cfg.FilterMACs) > 0 {
		parts = append(parts, "FILTER_MACS="+strings.Join(s.cfg.FilterMACs, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	if s.cfg.FilterBlocks {
		parts = append(parts, "FILTER_BLOCKS=true (listed cameras are excluded)")
	} else {
		parts = append(parts, "only the listed cameras are bridged")
	}
	return strings.Join(parts, "; ")
}

// excludedRow is one line of the "excluded right now" table, shared by
// the Settings page and the dashboard strip.
type excludedRow struct {
	MAC      string
	Nickname string
	Model    string
	IP       string
	Reason   string
}

func excludedRows(list []camera.Excluded) []excludedRow {
	rows := make([]excludedRow, 0, len(list))
	for _, e := range list {
		nick := e.Nickname
		if nick == "" {
			nick = e.MAC
		}
		rows = append(rows, excludedRow{
			MAC:      e.MAC,
			Nickname: nick,
			Model:    wyzeapi.CameraInfo{Model: e.Model}.ModelName(),
			IP:       e.IP,
			Reason:   e.Reason,
		})
	}
	return rows
}
