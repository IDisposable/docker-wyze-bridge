package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
)

func TestPruner_PruneOldRecordings(t *testing.T) {
	dir := t.TempDir()

	// Create directory structure
	camDir := filepath.Join(dir, "front_door", "2026", "04", "15")
	os.MkdirAll(camDir, 0755)

	// Old recording
	oldFile := filepath.Join(camDir, "14-00-00.mp4")
	os.WriteFile(oldFile, []byte("old"), 0644)
	os.Chtimes(oldFile, time.Now().Add(-96*time.Hour), time.Now().Add(-96*time.Hour))

	// Recent recording
	newFile := filepath.Join(camDir, "22-00-00.mp4")
	os.WriteFile(newFile, []byte("new"), 0644)

	// Non-mp4 file (should not be touched)
	txtFile := filepath.Join(camDir, "metadata.txt")
	os.WriteFile(txtFile, []byte("data"), 0644)
	os.Chtimes(txtFile, time.Now().Add(-96*time.Hour), time.Now().Add(-96*time.Hour))

	cfg := &config.Config{
		RecordPath:   dir + "/{cam_name}/%Y/%m/%d",
		RecordKeep:   48 * time.Hour,
		CamOverrides: make(map[string]config.CamOverride),
	}

	m := NewManager(cfg, nil, zerolog.Nop())
	m.prune()

	// Old mp4 should be deleted
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old recording should have been pruned")
	}

	// New mp4 should remain
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new recording should not have been pruned")
	}

	// txt should remain
	if _, err := os.Stat(txtFile); err != nil {
		t.Error("non-mp4 file should not have been pruned")
	}
}

func TestPruner_CleanEmptyDirs(t *testing.T) {
	dir := t.TempDir()

	// Create nested empty directory structure
	emptyDir := filepath.Join(dir, "cam", "2026", "01", "01")
	os.MkdirAll(emptyDir, 0755)

	// Put one old file to trigger walking
	oldFile := filepath.Join(emptyDir, "test.mp4")
	os.WriteFile(oldFile, []byte("x"), 0644)
	os.Chtimes(oldFile, time.Now().Add(-96*time.Hour), time.Now().Add(-96*time.Hour))

	cfg := &config.Config{
		RecordPath:   dir + "/{cam_name}/%Y/%m/%d",
		RecordKeep:   24 * time.Hour,
		CamOverrides: make(map[string]config.CamOverride),
	}

	m := NewManager(cfg, nil, zerolog.Nop())
	m.prune()

	// The deeply nested empty dirs should be cleaned up
	if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
		t.Error("empty directory should have been removed")
	}
}
