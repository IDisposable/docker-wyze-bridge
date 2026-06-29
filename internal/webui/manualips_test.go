package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"GW_WC_80482CB9EF6E":  "80482CB9EF6E",
		"80482CB9EF6E":        "80482CB9EF6E",
		"80:48:2C:B9:EF:6E":   "80482CB9EF6E",
		"aabbccddeeff":        "AABBCCDDEEFF",
		"GW_GC1_AABBCCDDEEFF": "AABBCCDDEEFF",
	}
	for in, want := range cases {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, want, got)
		}
	}
}

func TestLoadManualIPs_MissingFileIsEmpty(t *testing.T) {
	m, err := LoadManualIPs(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestLoadManualIPs_ParsesAndNormalizes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manual_ips.json")
	body := `{
		"GW_WC_80482CB9EF6E": "192.168.1.51",
		"GW_WC_80482CB92978": "192.168.1.52",
		"  ":                  "192.168.1.99",
		"GW_WC_EMPTY":         ""
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManualIPs(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := m.lookup("80:48:2C:B9:EF:6E"); got != "192.168.1.51" {
		t.Errorf("lookup by colon-MAC = %q, want 192.168.1.51", got)
	}
	if got := m.lookup("GW_WC_80482CB92978"); got != "192.168.1.52" {
		t.Errorf("lookup by full key = %q, want 192.168.1.52", got)
	}
	// Blank key and blank value entries are dropped.
	if len(m) != 2 {
		t.Errorf("expected 2 valid entries, got %d: %v", len(m), m)
	}
}

func TestManualIPs_LookupNil(t *testing.T) {
	var m ManualIPs
	if got := m.lookup("AABBCCDDEEFF"); got != "" {
		t.Errorf("nil lookup = %q, want empty", got)
	}
}
