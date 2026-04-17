package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWalkRoot(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/media/recordings/{cam_name}/%Y/%m/%d", "/media/recordings"},
		{"/media/recordings/{cam_name}", "/media/recordings"},
		{"/media/recordings", "/media/recordings"},
		{"%Y/recordings/{cam_name}", ""},
		{"", ""},
		{"/record/", "/record"},
	}
	for _, tt := range tests {
		if got := walkRoot(tt.in); got != tt.want {
			t.Errorf("walkRoot(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestStorageSampler_Refresh verifies the sampler picks up .mp4 files
// under <root>/<cam>/... layouts and sums them per camera.
func TestStorageSampler_Refresh(t *testing.T) {
	root := t.TempDir()
	// Lay out:
	//   root/front_door/2026-04-20/  (3 mp4 totaling 3000)
	//   root/backyard/2026-04-20/    (1 mp4 totaling 500)
	//   root/front_door/not_mp4.txt  (ignored)
	writeFile(t, filepath.Join(root, "front_door", "2026-04-20", "00-00-00.mp4"), 1000)
	writeFile(t, filepath.Join(root, "front_door", "2026-04-20", "00-01-00.mp4"), 1000)
	writeFile(t, filepath.Join(root, "front_door", "2026-04-20", "00-02-00.mp4"), 1000)
	writeFile(t, filepath.Join(root, "backyard", "2026-04-20", "00-00-00.mp4"), 500)
	writeFile(t, filepath.Join(root, "front_door", "random.txt"), 999)

	s := NewStorageSampler(root+"/{cam_name}/%Y-%m-%d", 60*60*time.Second)
	s.refresh()

	stats := s.Stats()
	if stats.TotalBytes != 3500 {
		t.Errorf("TotalBytes = %d, want 3500 (txt file should be ignored)", stats.TotalBytes)
	}
	if stats.PerCamera["front_door"] != 3000 {
		t.Errorf("PerCamera[front_door] = %d, want 3000", stats.PerCamera["front_door"])
	}
	if stats.PerCamera["backyard"] != 500 {
		t.Errorf("PerCamera[backyard] = %d, want 500", stats.PerCamera["backyard"])
	}
	if stats.LastRefresh.IsZero() {
		t.Error("LastRefresh not set")
	}
}

func writeFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
