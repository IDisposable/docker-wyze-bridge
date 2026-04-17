package webui

import (
	"fmt"
	"net/http"
	"strings"
)

// handleDashboardYAML emits a ready-to-paste Home Assistant Lovelace
// dashboard that references the MQTT discovery entities the bridge
// publishes. Users paste it into HA's raw dashboard editor, or — in
// the HA add-on installation — the run.sh writes it directly to
// /config/wyze_bridge_dashboard.yaml at startup.
//
// The entity IDs we reference match the unique_id patterns in
// internal/mqtt/discovery.go and internal/mqtt/metrics.go — if those
// change, update the template here.
func (s *Server) handleDashboardYAML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="wyze_bridge_dashboard.yaml"`)

	var b strings.Builder
	fmt.Fprintln(&b, "# Home Assistant Lovelace dashboard for docker-wyze-bridge")
	fmt.Fprintln(&b, "# Generated dynamically based on currently-discovered cameras.")
	fmt.Fprintln(&b, "# Paste into HA's raw dashboard editor (Dashboards → ⋮ → Raw configuration editor)")
	fmt.Fprintln(&b, "# or, if using the HA add-on, drop into /config/wyze_bridge_dashboard.yaml.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "title: Wyze Bridge")
	fmt.Fprintln(&b, "views:")
	fmt.Fprintln(&b, "  - title: Cameras")
	fmt.Fprintln(&b, "    path: cameras")
	fmt.Fprintln(&b, "    icon: mdi:cctv")
	fmt.Fprintln(&b, "    cards:")

	// Bridge summary card — glance of the six bridge sensors.
	fmt.Fprintln(&b, "      - type: glance")
	fmt.Fprintln(&b, "        title: Bridge Status")
	fmt.Fprintln(&b, "        state_color: true")
	fmt.Fprintln(&b, "        entities:")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_cameras")
	fmt.Fprintln(&b, "            name: Cameras")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_streaming")
	fmt.Fprintln(&b, "            name: Streaming")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_errored")
	fmt.Fprintln(&b, "            name: Errored")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_config_errors")
	fmt.Fprintln(&b, "            name: Config")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_recordings_size")
	fmt.Fprintln(&b, "            name: Storage")
	fmt.Fprintln(&b, "          - entity: sensor.bridge_uptime")
	fmt.Fprintln(&b, "            name: Uptime")

	// One picture-glance card per camera — snapshot background +
	// state badge + recording pill.
	for _, cam := range s.camMgr.Cameras() {
		name := cam.Name()
		info := cam.GetInfo()
		nick := info.Nickname
		if nick == "" {
			nick = name
		}
		entityBase := "wyze_" + strings.ToLower(info.MAC)

		fmt.Fprintln(&b, "      - type: picture-glance")
		fmt.Fprintf(&b, "        title: %s\n", yamlQuote(nick))
		fmt.Fprintf(&b, "        camera_image: camera.%s\n", entityBase)
		fmt.Fprintln(&b, "        show_state: false")
		fmt.Fprintln(&b, "        entities:")
		fmt.Fprintf(&b, "          - entity: binary_sensor.%s_recording\n", entityBase)
		fmt.Fprintln(&b, "            icon: mdi:record-rec")
		fmt.Fprintf(&b, "          - entity: switch.%s_audio\n", entityBase)
		fmt.Fprintf(&b, "          - entity: select.%s_quality\n", entityBase)
	}

	// Optional issues-log card — shows up regardless of whether there
	// are issues right now; users can delete the card if they don't
	// want it.
	fmt.Fprintln(&b, "      - type: markdown")
	fmt.Fprintln(&b, "        title: Diagnostics")
	fmt.Fprintln(&b, "        content: |")
	fmt.Fprintln(&b, "          - [Metrics page](/hassio_ingress/wyze-bridge/metrics)")
	fmt.Fprintln(&b, "          - [Prometheus export](/hassio_ingress/wyze-bridge/metrics.prom)")
	fmt.Fprintln(&b, "          - [Health JSON](/hassio_ingress/wyze-bridge/api/health)")

	_, _ = w.Write([]byte(b.String()))
}

// yamlQuote returns a YAML-safe double-quoted string when the input
// contains characters that would otherwise need escaping (quotes,
// colons, leading indicators). For plain alphanumeric strings it
// returns the input unchanged.
func yamlQuote(s string) string {
	needs := false
	for _, r := range s {
		switch r {
		case '"', '\'', ':', '{', '}', '[', ']', ',', '&', '*', '#', '?', '|', '<', '>', '=', '!', '%', '@', '`':
			needs = true
		}
		if needs {
			break
		}
	}
	if !needs && s != "" {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
