package gwell

import (
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if !c.Enabled {
		t.Error("expected Enabled=true by default")
	}
	if c.RTSPPort != 8564 {
		t.Errorf("RTSPPort = %d, want 8564", c.RTSPPort)
	}
	if c.ControlPort != 18564 {
		t.Errorf("ControlPort = %d, want 18564", c.ControlPort)
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"disabled bypasses validation", Config{Enabled: false, RTSPPort: 0}, ""},
		{"good defaults", DefaultConfig(), ""},
		{"bad rtsp port high", Config{Enabled: true, RTSPPort: 70000, ControlPort: 18564}, "RTSPPort"},
		{"bad rtsp port low", Config{Enabled: true, RTSPPort: 0, ControlPort: 18564}, "RTSPPort"},
		{"bad control port", Config{Enabled: true, RTSPPort: 8564, ControlPort: -1}, "ControlPort"},
		{"port collision", Config{Enabled: true, RTSPPort: 8000, ControlPort: 8000}, "must differ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestConfig_RTSPURL(t *testing.T) {
	c := Config{RTSPPort: 8564}
	got := c.RTSPURL("backyard")
	want := "rtsp://127.0.0.1:8564/backyard"
	if got != want {
		t.Errorf("RTSPURL = %q, want %q", got, want)
	}
}

func TestConfig_BaseURL(t *testing.T) {
	c := Config{ControlPort: 18564}
	if got := c.BaseURL(); got != "http://127.0.0.1:18564" {
		t.Errorf("BaseURL = %q", got)
	}
}

func TestConfig_ResolveBinary_FallbackToBareName(t *testing.T) {
	c := Config{BinaryPath: "/definitely/not/a/real/path/gwell-proxy-xyz"}
	got, err := c.ResolveBinary()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Falls back to bare name so exec.LookPath can try $PATH.
	if got != "gwell-proxy" && got != c.BinaryPath {
		t.Errorf("ResolveBinary = %q; expected bare 'gwell-proxy' fallback or exact path", got)
	}
}
