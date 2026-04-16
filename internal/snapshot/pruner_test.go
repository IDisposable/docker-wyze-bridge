package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestPruner_Prune(t *testing.T) {
	dir := t.TempDir()

	// Create an "old" file
	oldFile := filepath.Join(dir, "old.jpg")
	os.WriteFile(oldFile, []byte("old"), 0644)
	os.Chtimes(oldFile, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	// Create a "new" file
	newFile := filepath.Join(dir, "new.jpg")
	os.WriteFile(newFile, []byte("new"), 0644)

	// Create a non-jpg file (should not be touched)
	txtFile := filepath.Join(dir, "notes.txt")
	os.WriteFile(txtFile, []byte("notes"), 0644)
	os.Chtimes(txtFile, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	p := NewPruner(dir, 24*time.Hour, zerolog.Nop())
	p.prune()

	// Old jpg should be deleted
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old.jpg should have been pruned")
	}

	// New jpg should remain
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new.jpg should not have been pruned")
	}

	// Non-jpg should remain
	if _, err := os.Stat(txtFile); err != nil {
		t.Error("notes.txt should not have been pruned")
	}
}
