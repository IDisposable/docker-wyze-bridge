package recording

import (
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
)

func TestRecordPathForCamera(t *testing.T) {
	cfg := &config.Config{
		RecordPath:     "/media/recordings/{cam_name}/%Y/%m/%d",
		RecordFileName: "%H-%M-%S",
		CamOverrides:   make(map[string]config.CamOverride),
	}
	m := NewManager(cfg, zerolog.Nop())

	got := m.RecordPathForCamera("front_door")
	want := "/media/recordings/front_door/%Y/%m/%d"
	if got != want {
		t.Errorf("RecordPathForCamera() = %q, want %q", got, want)
	}
}

func TestRecordFileNameForCamera(t *testing.T) {
	cfg := &config.Config{
		RecordPath:     "/media/recordings/{cam_name}/%Y/%m/%d",
		RecordFileName: "%H-%M-%S",
		CamOverrides:   make(map[string]config.CamOverride),
	}
	m := NewManager(cfg, zerolog.Nop())

	got := m.RecordFileNameForCamera("front_door")
	// Should contain both path and filename with valid time vars
	if got == "" {
		t.Error("should return non-empty path")
	}
	// The default config has %Y, %m, %d, %H, %M, %S so should be valid
}

func TestRecordFileNameValidation(t *testing.T) {
	// Missing time variables should auto-append _%s
	cfg := &config.Config{
		RecordPath:     "/media/recordings/{cam_name}",
		RecordFileName: "recording",
		CamOverrides:   make(map[string]config.CamOverride),
	}
	m := NewManager(cfg, zerolog.Nop())

	got := m.RecordFileNameForCamera("test")
	if got[len(got)-3:] != "_%s" {
		t.Errorf("should auto-append %%s, got %q", got)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.b)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

func TestIsEnabled(t *testing.T) {
	b := true
	cfg := &config.Config{
		RecordAll:    false,
		CamOverrides: map[string]config.CamOverride{
			"FRONT_DOOR": {Record: &b},
		},
	}
	m := NewManager(cfg, zerolog.Nop())

	if !m.IsEnabled("front_door") {
		t.Error("front_door should have recording enabled")
	}
	if m.IsEnabled("backyard") {
		t.Error("backyard should not have recording enabled")
	}
}

// Suppress unused import for time
var _ = time.Now
