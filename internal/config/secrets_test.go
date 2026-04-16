package config

import (
	"testing"
)

func TestSecret_EnvVar(t *testing.T) {
	t.Setenv("TEST_SECRET", "myvalue")
	if got := secret("TEST_SECRET"); got != "myvalue" {
		t.Errorf("secret() = %q, want myvalue", got)
	}
}

func TestSecret_Missing(t *testing.T) {
	if got := secret("TOTALLY_MISSING_SECRET_XYZ"); got != "" {
		t.Errorf("missing secret should be empty, got %q", got)
	}
}

func TestSecret_Trimming(t *testing.T) {
	t.Setenv("TEST_SECRET_TRIM", "  spaced  \n")
	if got := secret("TEST_SECRET_TRIM"); got != "spaced" {
		t.Errorf("secret() = %q, want trimmed", got)
	}
}

func TestSecretWithAlias_Primary(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "primary")
	t.Setenv("ALIAS_KEY", "alias")
	if got := secretWithAlias("PRIMARY_KEY", "ALIAS_KEY"); got != "primary" {
		t.Errorf("should prefer primary, got %q", got)
	}
}

func TestSecretWithAlias_Fallback(t *testing.T) {
	t.Setenv("ALIAS_ONLY", "from_alias")
	if got := secretWithAlias("MISSING_PRIMARY_XYZ", "ALIAS_ONLY"); got != "from_alias" {
		t.Errorf("should fall back to alias, got %q", got)
	}
}

func TestSecretWithAlias_Both_Missing(t *testing.T) {
	if got := secretWithAlias("MISS_A_XYZ", "MISS_B_XYZ"); got != "" {
		t.Errorf("both missing should be empty, got %q", got)
	}
}

func TestAPIIDAlias(t *testing.T) {
	// Simulate the backward-compat: WYZE_API_ID not set, but API_ID is
	t.Setenv("WYZE_API_ID", "") // clear in case the real env has it
	t.Setenv("API_ID", "old-style-id")
	got := secretWithAlias("WYZE_API_ID", "API_ID")
	if got != "old-style-id" {
		t.Errorf("API_ID alias = %q, want old-style-id", got)
	}
}
