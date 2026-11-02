package sddownload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, Pairing, Request) {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(helper)
	p := Pairing{IP: "192.168.2.3", UID: "ABCDEFGHIJKLMNOPQRST", ENR: strings.Repeat("x", 16), MAC: "aa:bb:cc:dd:ee:ff", Model: "WYZE_CAKP2JFUS", DTLS: true}
	return s, p, Request{Date: "20260228", Hour: 9, Minute: 12}
}

func arguments(t *testing.T, args []string) map[string]string {
	t.Helper()
	if len(args) != 11 || args[0] != "--live" {
		t.Fatalf("helper argument shape: %v", args)
	}
	m := make(map[string]string)
	for i := 1; i < len(args); i += 2 {
		m[args[i]] = args[i+1]
	}
	if m["--max-bytes"] != "16777216" {
		t.Fatal("missing size limit")
	}
	return m
}

func writeSuccess(t *testing.T, m map[string]string) {
	t.Helper()
	if err := os.WriteFile(m["--media-output"], []byte("test-clip"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"operation": "download_sd_file", "phase": "download_complete", "recording_path": m["--download"], "downloaded_bytes": 9, "elapsed_seconds": 0.1})
	if err := os.WriteFile(m["--output"], data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadOwnsFileAndLockUntilClose(t *testing.T) {
	s, p, r := fixture(t)
	var scratch string
	s.run = func(ctx context.Context, helper string, args ...string) error {
		if helper != s.helper {
			t.Fatal("helper path changed")
		}
		m := arguments(t, args)
		scratch = filepath.Dir(m["--credentials"])
		for path, mode := range map[string]os.FileMode{scratch: 0700, m["--credentials"]: 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("private permissions: %v", err)
			}
		}
		data, err := os.ReadFile(m["--credentials"])
		if err != nil {
			t.Fatal(err)
		}
		var pairing map[string]string
		if json.Unmarshal(data, &pairing) != nil || len(pairing) != 5 || pairing["mac"] != "AABBCCDDEEFF" || pairing["uid"] != p.UID {
			t.Fatal("pairing contract changed")
		}
		writeSuccess(t, m)
		return nil
	}
	f, err := s.Download(context.Background(), p, r)
	if err != nil {
		t.Fatal(err)
	}
	if f.Size != 9 || f.Filename != "20260228-0912.mp4" {
		t.Fatal("attachment metadata")
	}
	if _, err = s.Download(context.Background(), p, r); err != ErrBusy {
		t.Fatalf("second export: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "test-clip" {
		t.Fatalf("media: %q %v", data, err)
	}
	if _, err = os.Stat(scratch); err != nil {
		t.Fatal("scratch removed before response finished")
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatal("scratch retained")
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = s.Download(context.Background(), p, r)
	if err != nil {
		t.Fatal("lock not released", err)
	}
	_ = f.Close()
}

func TestDownloadRejectsIncompleteOrUnsafeOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		want  error
		alter func(map[string]string)
	}{
		{"missing report", ErrInvalidResult, func(m map[string]string) { _ = os.Remove(m["--output"]) }},
		{"duplicate result field", ErrInvalidResult, func(m map[string]string) {
			_ = os.WriteFile(m["--output"], []byte(`{"operation":"download_sd_file","operation":"other"}`), 0600)
		}},
		{"wrong path", ErrInvalidResult, func(m map[string]string) {
			_ = os.WriteFile(m["--output"], []byte(`{"operation":"download_sd_file","recording_path":"/other"}`), 0600)
		}},
		{"missing file", ErrNotFound, func(m map[string]string) {
			data, _ := json.Marshal(map[string]any{"operation": "download_sd_file", "recording_path": m["--download"], "error": "file does not exist"})
			_ = os.WriteFile(m["--output"], data, 0600)
		}},
		{"size mismatch", ErrIncomplete, func(m map[string]string) { _ = os.WriteFile(m["--media-output"], []byte("short"), 0600) }},
		{"public media", ErrIncomplete, func(m map[string]string) { _ = os.Chmod(m["--media-output"], 0644) }},
		{"public report", ErrInvalidResult, func(m map[string]string) { _ = os.Chmod(m["--output"], 0644) }},
		{"symlink media", ErrIncomplete, func(m map[string]string) {
			_ = os.Remove(m["--media-output"])
			_ = os.Symlink(m["--credentials"], m["--media-output"])
		}},
		{"fifo report", ErrInvalidResult, func(m map[string]string) { _ = os.Remove(m["--output"]); _ = syscall.Mkfifo(m["--output"], 0600) }},
		{"oversized report", ErrInvalidResult, func(m map[string]string) { _ = os.WriteFile(m["--output"], []byte(strings.Repeat(" ", 16385)), 0600) }},
		{"partial phase", ErrIncomplete, func(m map[string]string) {
			data, _ := json.Marshal(map[string]any{"operation": "download_sd_file", "recording_path": m["--download"], "phase": "transfer", "downloaded_bytes": 9})
			_ = os.WriteFile(m["--output"], data, 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, r := fixture(t)
			var scratch string
			s.run = func(_ context.Context, _ string, args ...string) error {
				m := arguments(t, args)
				scratch = filepath.Dir(m["--output"])
				writeSuccess(t, m)
				tc.alter(m)
				return nil
			}
			if f, err := s.Download(context.Background(), p, r); !errors.Is(err, tc.want) {
				if f != nil {
					_ = f.Close()
				}
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(scratch); !os.IsNotExist(err) {
				t.Fatal("failure retained scratch")
			}
			if s.busy.Load() {
				t.Fatal("failure retained lock")
			}
		})
	}
}

func TestDownloadExitFailureDiscardsSuccessfulLookingOutput(t *testing.T) {
	s, p, r := fixture(t)
	s.run = func(_ context.Context, _ string, args ...string) error {
		writeSuccess(t, arguments(t, args))
		return &exec.ExitError{}
	}
	if _, err := s.Download(context.Background(), p, r); err != ErrIncomplete {
		t.Fatal(err)
	}
}

func TestDownloadTimeoutKillsHelperAndCleans(t *testing.T) {
	s, p, r := fixture(t)
	if err := os.WriteFile(s.helper, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s.timeout = 30 * time.Millisecond
	start := time.Now()
	if _, err := s.Download(context.Background(), p, r); err != ErrTimeout {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second || s.busy.Load() {
		t.Fatal("timed out helper retained resources")
	}
}

func TestDownloadDoesNotInheritAccountSecrets(t *testing.T) {
	s, p, r := fixture(t)
	t.Setenv("WYZE_PASSWORD", "fixture-account-secret")
	script := `#!/bin/sh
test -z "${WYZE_PASSWORD+x}" || exit 1
shift
while [ "$#" -gt 0 ]; do
 case "$1" in
 --download) remote="$2";;
 --media-output) media="$2";;
 --output) result="$2";;
 esac
 shift 2
done
umask 077
printf test-clip > "$media"
printf '{"operation":"download_sd_file","phase":"download_complete","recording_path":"%s","downloaded_bytes":9}' "$remote" > "$result"
`
	if err := os.WriteFile(s.helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := s.Download(context.Background(), p, r)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}
