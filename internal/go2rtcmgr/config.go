package go2rtcmgr

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Go2RTCConfig is the YAML config for go2rtc.
type Go2RTCConfig struct {
	Log     LogConfig              `yaml:"log"`
	API     APIConfig              `yaml:"api"`
	RTSP    RTSPConfig             `yaml:"rtsp"`
	WebRTC  WebRTCConfig           `yaml:"webrtc"`
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
}

// NewConfigBuilder creates a new builder for go2rtc config.
func NewConfigBuilder(logLevel, stunServer, wbIP string) *ConfigBuilder {
	return &ConfigBuilder{
		logLevel:   logLevel,
		stunServer: stunServer,
		wbIP:       wbIP,
	}
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
			Listen: ":1984",
			Origin: "*", // needed for bridge WebUI on :5080 to use WebRTC player
		},
		RTSP: RTSPConfig{Listen: ":8554"},
		WebRTC: WebRTCConfig{
			Listen: ":8889",
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
			fmt.Sprintf("%s:8889", b.wbIP),
		}
	}

	// Apply STREAM_AUTH to RTSP config
	// If there's a single global auth entry (no per-camera restriction), set it on RTSP
	if len(b.streamAuth) == 1 && len(b.streamAuth[0].Cameras) == 0 {
		cfg.RTSP.Username = b.streamAuth[0].Username
		cfg.RTSP.Password = b.streamAuth[0].Password
	}

	if len(b.streams) > 0 {
		cfg.Streams = make(map[string]interface{}, len(b.streams))
	}
	for _, s := range b.streams {
		if s.Record && s.RecordPath != "" {
			// Full config with recording directives
			cfg.Streams[s.Name] = map[string]interface{}{
				"sources":         []string{s.URL},
				"record":          true,
				"record_path":     s.RecordPath,
				"record_duration": s.RecordDuration,
			}
		} else {
			cfg.Streams[s.Name] = []string{s.URL}
		}
	}

	return cfg
}

// WriteConfig writes the go2rtc YAML config file.
func (b *ConfigBuilder) WriteConfig(path string) error {
	cfg := b.Build()

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

	return os.WriteFile(path, data, 0644)
}
