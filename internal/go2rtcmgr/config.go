package go2rtcmgr

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Go2RTCConfig is the YAML config for go2rtc.
type Go2RTCConfig struct {
	Log     LogConfig              `yaml:"log"`
	API     APIConfig              `yaml:"api"`
	RTSP    RTSPConfig             `yaml:"rtsp"`
	WebRTC  WebRTCConfig           `yaml:"webrtc"`
	FFmpeg  map[string]string      `yaml:"ffmpeg,omitempty"`
	Streams map[string]interface{} `yaml:"streams,omitempty"`
	Record  *RecordGlobalConfig    `yaml:"record,omitempty"`
}

// LogConfig controls go2rtc logging.
type LogConfig struct {
	Level string `yaml:"level"`
}

// APIConfig controls the go2rtc HTTP API.
type APIConfig struct {
	Listen string `yaml:"listen"`
	Origin string `yaml:"origin"`
	// Username/Password enable HTTP Basic auth on /api/*. Empty =
	// unauthenticated (go2rtc's historical default). Loopback traffic
	// from wyze-bridge still authenticates the same way — the bridge
	// composes the same creds into its APIClient.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// RTSPConfig controls the go2rtc RTSP server.
type RTSPConfig struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

// WebRTCConfig controls WebRTC settings.
type WebRTCConfig struct {
	Listen     string      `yaml:"listen"`
	ICEServers []ICEServer `yaml:"ice_servers,omitempty"`
	Candidates []string    `yaml:"candidates,omitempty"`
}

// ICEServer represents a STUN/TURN server.
type ICEServer struct {
	URLs []string `yaml:"urls"`
}

// RecordGlobalConfig controls go2rtc's global recording.
type RecordGlobalConfig struct {
	OutputDir string `yaml:"output,omitempty"`
}

// StreamEntry contains the config for a single camera stream in go2rtc.
type StreamEntry struct {
	Name           string
	URL            string
	Record         bool
	RecordPath     string
	RecordDuration string
	RecordKeep     int // seconds, 0 = forever
}

// StreamAuthEntry represents a parsed STREAM_AUTH user credential.
type StreamAuthEntry struct {
	Username string
	Password string
	Cameras  []string // empty = all cameras
}

// ConfigBuilder builds go2rtc YAML configuration.
type ConfigBuilder struct {
	logLevel   string
	stunServer string
	wbIP       string
	streams    []StreamEntry
	streamAuth []StreamAuthEntry

	// Listener ports (defaults populated by NewConfigBuilder).
	apiPort    int
	rtspPort   int
	webrtcPort int

	// go2rtc /api/* Basic-auth. Empty = no auth (default).
	apiUsername string
	apiPassword string

	// Extra streams supplied by the operator via GO2RTC_EXTRA_STREAMS
	// or the HA add-on's `go2rtc.extra_streams` option. Rendered into
	// the `streams:` map alongside camera streams; last-writer wins on
	// name collision (extras loaded before camera streams so a camera
	// name always beats an operator-defined stub).
	extraStreams []StreamEntry
	// extraYAML is appended verbatim to the generated YAML after the
	// managed section. Escape hatch for anything the typed struct
	// doesn't model (custom go2rtc `publish:`/`hass:` blocks, ONVIF
	// discovery, etc.). No validation beyond parse-time YAML checks.
	extraYAML string
}

// NewConfigBuilder creates a new builder for go2rtc config.
func NewConfigBuilder(logLevel, stunServer, wbIP string) *ConfigBuilder {
	return &ConfigBuilder{
		logLevel:   logLevel,
		stunServer: stunServer,
		wbIP:       wbIP,
		apiPort:    1984,
		rtspPort:   8554,
		webrtcPort: 8889,
	}
}

// SetAPIPort overrides the go2rtc HTTP API listen port. Zero = keep default.
func (b *ConfigBuilder) SetAPIPort(port int) {
	if port > 0 {
		b.apiPort = port
	}
}

// SetRTSPPort overrides the go2rtc RTSP listen port. Zero = keep default.
func (b *ConfigBuilder) SetRTSPPort(port int) {
	if port > 0 {
		b.rtspPort = port
	}
}

// SetWebRTCPort overrides the go2rtc WebRTC HTTP listen port. Zero = keep default.
func (b *ConfigBuilder) SetWebRTCPort(port int) {
	if port > 0 {
		b.webrtcPort = port
	}
}

// APIPort returns the currently configured API port. main.go uses
// this to build the APIClient base URL after calling SetAPIPort.
func (b *ConfigBuilder) APIPort() int { return b.apiPort }

// SetAPIAuth enables HTTP Basic auth on go2rtc's /api/* endpoints.
// Both fields must be non-empty to take effect; an empty pair is a no-op
// so the operator can leave the env vars unset without accidentally
// enabling auth with blank credentials.
func (b *ConfigBuilder) SetAPIAuth(username, password string) {
	if username == "" || password == "" {
		return
	}
	b.apiUsername = username
	b.apiPassword = password
}

// APIUsername / APIPassword expose the resolved API auth so main.go can
// mint an APIClient that speaks the same credentials to go2rtc.
func (b *ConfigBuilder) APIUsername() string { return b.apiUsername }
func (b *ConfigBuilder) APIPassword() string { return b.apiPassword }

// AddExtraStream registers an operator-supplied stream (from
// GO2RTC_EXTRA_STREAMS or HA options). Same wire format as a camera
// stream but tracked separately so future ClearStreams calls (used
// for per-discovery-cycle regeneration) don't wipe them.
func (b *ConfigBuilder) AddExtraStream(entry StreamEntry) {
	b.extraStreams = append(b.extraStreams, entry)
}

// AppendRawYAML appends a fragment to the emitted YAML after the
// managed section. Called last during WriteConfig; fragment is not
// re-indented or re-validated by us — pass a valid go2rtc YAML block.
func (b *ConfigBuilder) AppendRawYAML(fragment string) {
	b.extraYAML = fragment
}

// ParseExtraStreams parses the GO2RTC_EXTRA_STREAMS format:
//
//	"name=source,name=source[;name=source]"
//
// Comma OR semicolon separates entries; each entry is name=source_url.
// Names are trimmed; sources are taken as-is (may contain any URL/scheme
// go2rtc understands). Entries missing an `=` are skipped silently — the
// intent is that operators can hand the string back through unchanged from
// the HA add-on without a schema round-trip.
func ParseExtraStreams(raw string) []StreamEntry {
	if raw == "" {
		return nil
	}
	// Normalize semicolons to commas so we accept either separator.
	normalized := strings.ReplaceAll(raw, ";", ",")
	var entries []StreamEntry
	for _, tok := range strings.Split(normalized, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		eq := strings.Index(tok, "=")
		if eq <= 0 || eq == len(tok)-1 {
			continue
		}
		name := strings.TrimSpace(tok[:eq])
		src := strings.TrimSpace(tok[eq+1:])
		if name == "" || src == "" {
			continue
		}
		entries = append(entries, StreamEntry{Name: name, URL: src})
	}
	return entries
}

// AddStream adds a camera stream to the config.
func (b *ConfigBuilder) AddStream(entry StreamEntry) {
	b.streams = append(b.streams, entry)
}

// ClearStreams removes all streams.
func (b *ConfigBuilder) ClearStreams() {
	b.streams = nil
}

// SetStreamAuth sets the parsed STREAM_AUTH entries.
func (b *ConfigBuilder) SetStreamAuth(entries []StreamAuthEntry) {
	b.streamAuth = entries
}

// ParseStreamAuth parses the STREAM_AUTH format: "user:pass@cam1,cam2|user2:pass2"
func ParseStreamAuth(raw string) []StreamAuthEntry {
	if raw == "" {
		return nil
	}
	var entries []StreamAuthEntry
	for _, segment := range strings.Split(raw, "|") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		// Split on @ to separate creds from camera list
		credPart := segment
		var cams []string
		if atIdx := strings.Index(segment, "@"); atIdx >= 0 {
			credPart = segment[:atIdx]
			camStr := segment[atIdx+1:]
			for _, c := range strings.Split(camStr, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					cams = append(cams, c)
				}
			}
		}
		// Split creds on : for user:pass
		parts := strings.SplitN(credPart, ":", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, StreamAuthEntry{
			Username: parts[0],
			Password: parts[1],
			Cameras:  cams,
		})
	}
	return entries
}

// Build generates the Go2RTCConfig struct.
func (b *ConfigBuilder) Build() *Go2RTCConfig {
	cfg := &Go2RTCConfig{
		Log: LogConfig{Level: b.logLevel},
		API: APIConfig{
			Listen:   fmt.Sprintf(":%d", b.apiPort),
			Origin:   "*", // needed for bridge WebUI on :5080 to use WebRTC player
			Username: b.apiUsername,
			Password: b.apiPassword,
		},
		RTSP: RTSPConfig{Listen: fmt.Sprintf(":%d", b.rtspPort)},
		WebRTC: WebRTCConfig{
			Listen: fmt.Sprintf(":%d", b.webrtcPort),
		},
		// Named templates keep raw arguments out of dynamically registered
		// URLs: go2rtc intentionally rejects sources containing spaces.
		FFmpeg: map[string]string{
			// Enable bounded HEVC frame decoding without discarding probe packets.
			"wyze_lake_preview_input": "-rtsp_transport tcp -threads:v 2 -timeout 15000000 -i {input}",
			// Avoid filling RTP timestamp gaps with repeated stale frames. Keep
			// source timestamp precision and two-second keyframes in low light.
			"wyze_lake_preview": "-threads 2 -fps_mode:v vfr -enc_time_base:v demux -force_key_frames expr:if(isnan(prev_forced_t),1,gte(t,prev_forced_t+2))",
			// WebRTC can deliver recovered RTP packets out of order even
			// though the local RTSP hop uses TCP. Copy the native video
			// through a bounded reorder queue before exposing either output.
			"wyze_lake_receive": "-rtsp_transport tcp -reorder_queue_size 512 -max_delay 1000000 -timeout 15000000 -i {input}",
		},
		// Streams is nil when empty so YAML omits the key entirely.
		// go2rtc parses an empty flow-style `streams: {}` unreliably.
	}

	if b.stunServer != "" {
		cfg.WebRTC.ICEServers = []ICEServer{
			{URLs: []string{b.stunServer}},
		}
	}

	if b.wbIP != "" {
		cfg.WebRTC.Candidates = []string{
			fmt.Sprintf("%s:%d", b.wbIP, b.webrtcPort),
		}
	}

	// Apply STREAM_AUTH to RTSP config
	// If there's a single global auth entry (no per-camera restriction), set it on RTSP
	if len(b.streamAuth) == 1 && len(b.streamAuth[0].Cameras) == 0 {
		cfg.RTSP.Username = b.streamAuth[0].Username
		cfg.RTSP.Password = b.streamAuth[0].Password
	}

	total := len(b.streams) + len(b.extraStreams)
	if total > 0 {
		cfg.Streams = make(map[string]interface{}, total)
	}
	// Extras first so a camera name always overrides an operator-defined
	// stub — cameras are the primary product; extras are auxiliary.
	for _, s := range b.extraStreams {
		var sources []string
		if s.URL != "" {
			sources = []string{s.URL}
		} else {
			sources = []string{}
		}
		cfg.Streams[s.Name] = sources
	}
	for _, s := range b.streams {
		// Empty URL means publish-only slot (Gwell cameras: go2rtc
		// reserves the stream name with no source, accepts RTSP PUBLISH
		// into it). YAML form is `name: []` — not `name: [""]`, which
		// go2rtc would treat as a single malformed source and spam
		// errors at startup.
		var sources []string
		if s.URL != "" {
			sources = []string{s.URL}
		} else {
			sources = []string{}
		}
		if s.Record && s.RecordPath != "" {
			cfg.Streams[s.Name] = map[string]interface{}{
				"sources":         sources,
				"record":          true,
				"record_path":     s.RecordPath,
				"record_duration": s.RecordDuration,
			}
		} else {
			cfg.Streams[s.Name] = sources
		}
	}

	return cfg
}

// WriteConfig writes the go2rtc YAML config file.
func (b *ConfigBuilder) WriteConfig(path string) error {
	cfg := b.Build()
	extraYAML := b.extraYAML
	// Adding our named templates must not create a second ffmpeg root when
	// an operator already supplied custom FFmpeg settings. go2rtc rejects
	// duplicate YAML keys. Merge only the missing templates into that block.
	if strings.TrimSpace(extraYAML) != "" {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(extraYAML), &doc); err != nil {
			return fmt.Errorf("parse extra go2rtc config: %w", err)
		}
		if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			root := doc.Content[0]
			// Decode the effective values so YAML merge keys and their
			// precedence are respected when checking operator overrides.
			var operator struct {
				FFmpeg map[string]string `yaml:"ffmpeg"`
			}
			if err := doc.Decode(&operator); err != nil {
				return fmt.Errorf("parse extra FFmpeg config: %w", err)
			}
			var block *yaml.Node
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i].Value == "ffmpeg" {
					block = root.Content[i+1]
					break
				}
			}
			if block == nil && operator.FFmpeg != nil {
				// An inherited root block needs a local override with the
				// same values; leave its source anchor and other aliases alone.
				block = &yaml.Node{}
				if err := block.Encode(operator.FFmpeg); err != nil {
					return fmt.Errorf("encode inherited FFmpeg config: %w", err)
				}
				root.Content = append(root.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ffmpeg"}, block)
			}
			if block != nil {
				if block.Kind == yaml.AliasNode {
					// Do not insert templates into an anchor shared with
					// unrelated operator configuration.
					var local yaml.Node
					if err := local.Encode(operator.FFmpeg); err != nil {
						return fmt.Errorf("encode aliased FFmpeg config: %w", err)
					}
					local.HeadComment, local.LineComment, local.FootComment = block.HeadComment, block.LineComment, block.FootComment
					*block = local
				}
				if block.Kind == yaml.ScalarNode && block.Tag == "!!null" {
					// An empty operator block keeps go2rtc's defaults.
					block.Kind, block.Tag, block.Value = yaml.MappingNode, "!!map", ""
				}
				if block.Kind != yaml.MappingNode {
					return fmt.Errorf("extra go2rtc ffmpeg config must be a mapping")
				}
				keys := make([]string, 0, len(cfg.FFmpeg))
				for key := range cfg.FFmpeg {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					value := cfg.FFmpeg[key]
					_, present := operator.FFmpeg[key]
					if !present {
						block.Content = append(block.Content,
							&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
							&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
					}
				}
				merged, err := yaml.Marshal(&doc)
				if err != nil {
					return fmt.Errorf("merge extra FFmpeg config: %w", err)
				}
				extraYAML = string(merged)
				cfg.FFmpeg = nil
			}
		}
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Add recording config as comments/structured entries for streams that need it
	// go2rtc handles recording per-stream via its API, but we set global config here
	var recordStreams []StreamEntry
	for _, s := range b.streams {
		if s.Record {
			recordStreams = append(recordStreams, s)
		}
	}

	if len(recordStreams) > 0 {
		// Append recording configuration as YAML
		var extra strings.Builder
		extra.WriteString("\n# Recording configuration\n")
		for _, s := range recordStreams {
			extra.WriteString(fmt.Sprintf("# Stream %q: record to %s\n", s.Name, s.RecordPath))
		}
		data = append(data, []byte(extra.String())...)
	}

	// Operator's GO2RTC_EXTRA_YAML escape hatch. Emitted after a
	// visible marker so anyone reading the file understands what's
	// managed vs. what came in verbatim.
	if strings.TrimSpace(extraYAML) != "" {
		data = append(data, []byte(
			"\n# --- managed by wyze-bridge; anything above is regenerated ---\n"+
				"# GO2RTC_EXTRA_YAML (operator input with missing receive templates merged):\n")...)
		if !strings.HasSuffix(extraYAML, "\n") {
			extraYAML += "\n"
		}
		data = append(data, []byte(extraYAML)...)
	}

	return os.WriteFile(path, data, 0644)
}
