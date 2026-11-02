package config

import "testing"

func TestSDDownloadHelperConfiguration(t *testing.T) {
	for _, path := range []string{"", "/usr/local/bin/wyze-sd-download"} {
		t.Setenv("SD_DOWNLOAD_HELPER", path)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SDDownloadHelper != path {
			t.Fatalf("helper = %q, want %q", cfg.SDDownloadHelper, path)
		}
	}
}
