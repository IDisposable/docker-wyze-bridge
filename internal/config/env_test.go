package config

import (
	"testing"
	"time"
)

func TestEnv(t *testing.T) {
	t.Setenv("TEST_ENV_VAL", "hello")
	if got := env("TEST_ENV_VAL", "default"); got != "hello" {
		t.Errorf("env() = %q, want %q", got, "hello")
	}
	if got := env("TEST_ENV_MISSING", "default"); got != "default" {
		t.Errorf("env() = %q, want %q", got, "default")
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		val      string
		fallback bool
		want     bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", true, false},
		{"FALSE", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		{"none", true, false},
		{"", true, true},
		{"", false, false},
		{"garbage", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.val, func(t *testing.T) {
			if tt.val != "" {
				t.Setenv("TEST_BOOL", tt.val)
			}
			got := envBool("TEST_BOOL", tt.fallback)
			if got != tt.want {
				t.Errorf("envBool(%q, %v) = %v, want %v", tt.val, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if got := envInt("TEST_INT", 0); got != 42 {
		t.Errorf("envInt() = %d, want 42", got)
	}

	t.Setenv("TEST_INT", "not_a_number")
	if got := envInt("TEST_INT", 99); got != 99 {
		t.Errorf("envInt() = %d, want 99", got)
	}

	if got := envInt("TEST_INT_MISSING", 7); got != 7 {
		t.Errorf("envInt() = %d, want 7", got)
	}
}

func TestEnvFloat(t *testing.T) {
	t.Setenv("TEST_FLOAT", "3.14")
	if got := envFloat("TEST_FLOAT", 0); got != 3.14 {
		t.Errorf("envFloat() = %f, want 3.14", got)
	}

	t.Setenv("TEST_FLOAT", "bad")
	if got := envFloat("TEST_FLOAT", 2.0); got != 2.0 {
		t.Errorf("envFloat() = %f, want 2.0", got)
	}
}

func TestEnvList(t *testing.T) {
	t.Setenv("TEST_LIST", " cam1 , cam2,  cam3 ")
	got := envList("TEST_LIST")
	if len(got) != 3 || got[0] != "CAM1" || got[1] != "CAM2" || got[2] != "CAM3" {
		t.Errorf("envList() = %v", got)
	}

	got = envList("TEST_LIST_MISSING")
	if got != nil {
		t.Errorf("envList(missing) = %v, want nil", got)
	}
}

func TestEnvDuration(t *testing.T) {
	tests := []struct {
		val  string
		want time.Duration
	}{
		{"60s", 60 * time.Second},
		{"5m", 5 * time.Minute},
		{"72h", 72 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"120", 120 * time.Second},
		{"", 99 * time.Second},
		{"bad", 99 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.val, func(t *testing.T) {
			if tt.val != "" {
				t.Setenv("TEST_DUR", tt.val)
			}
			got := envDuration("TEST_DUR", 99*time.Second)
			if got != tt.want {
				t.Errorf("envDuration(%q) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}
