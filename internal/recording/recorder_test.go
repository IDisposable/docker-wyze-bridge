package recording

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/issues"
)

// TestBuildFFmpegArgs pins the exact ffmpeg argv we spawn per camera.
// The segment muxer with -strftime gets us the template substitution
// we want, -c copy keeps CPU cost near zero, and -rtsp_transport tcp
// avoids the packet-loss issues that plagued the UDP path on the
// Python bridge. Any change here should be intentional.
func TestBuildFFmpegArgs(t *testing.T) {
	cfg := &config.Config{
		RecordPath:     "/tmp/rec/{cam_name}/%Y/%m/%d",
		RecordFileName: "%H-%M-%S",
		RecordLength:   45 * time.Second,
	}
	m := NewManager(cfg, nil, zerolog.Nop())

	target, args := m.buildFFmpegArgs("front_door")

	wantTarget := "/tmp/rec/front_door/%Y/%m/%d/%H-%M-%S.mp4"
	if target != wantTarget {
		t.Errorf("target = %q, want %q", target, wantTarget)
	}

	joined := strings.Join(args, " ")
	mustContain := []string{
		"-rtsp_transport tcp",
		"-i rtsp://127.0.0.1:8554/front_door",
		"-c:v copy",
		"-c:a aac",
		"-f segment",
		"-segment_time 45",
		"-reset_timestamps 1",
		"-strftime 1",
		wantTarget,
	}
	for _, frag := range mustContain {
		if !strings.Contains(joined, frag) {
			t.Errorf("argv missing %q\nfull: %s", frag, joined)
		}
	}
}

// TestBuildFFmpegArgs_SegmentDefault covers the zero-duration fallback.
// If the user leaves RECORD_LENGTH unset (or sets it to 0) we don't
// want ffmpeg segmenting every frame.
func TestBuildFFmpegArgs_SegmentDefault(t *testing.T) {
	cfg := &config.Config{
		RecordPath:     "/tmp/rec",
		RecordFileName: "%s",
		RecordLength:   0,
	}
	m := NewManager(cfg, nil, zerolog.Nop())
	_, args := m.buildFFmpegArgs("cam")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-segment_time 60") {
		t.Errorf("expected default segment_time=60 when RECORD_LENGTH is zero; got: %s", joined)
	}
}

// TestBuildFFmpegArgs_GlobalOverride covers the RECORD_CMD path: a
// valid template replaces the default argv entirely.
func TestBuildFFmpegArgs_GlobalOverride(t *testing.T) {
	cfg := &config.Config{
		RecordPath:     "/tmp/rec/{cam_name}/%Y-%m-%d",
		RecordFileName: "%H-%M-%S",
		RecordLength:   45 * time.Second,
		RecordCmd:      "ffmpeg -i {rtsp_url} -c:v libx264 -f segment -segment_time {segment_sec} {output_stem}.mkv",
	}
	m := NewManager(cfg, nil, zerolog.Nop())

	_, args := m.buildFFmpegArgs("garage")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "libx264") {
		t.Errorf("expected override argv to include libx264; got: %s", joined)
	}
	if !strings.Contains(joined, "-segment_time 45") {
		t.Errorf("expected segment_sec substitution; got: %s", joined)
	}
	if !strings.Contains(joined, "/tmp/rec/garage/%Y-%m-%d/%H-%M-%S.mkv") {
		t.Errorf("expected output_stem + .mkv substitution; got: %s", joined)
	}
	if strings.Contains(joined, "-c copy") {
		t.Errorf("override should not include the default -c copy: %s", joined)
	}
}

// TestBuildFFmpegArgs_PerCameraOverrideWins verifies a per-camera
// RECORD_CMD_<CAM> takes precedence over the global RECORD_CMD.
func TestBuildFFmpegArgs_PerCameraOverrideWins(t *testing.T) {
	perCam := "ffmpeg -i {rtsp_url} -vcodec h264_nvenc {output}"
	cfg := &config.Config{
		RecordPath:     "/tmp/rec/{cam_name}",
		RecordFileName: "seg",
		RecordLength:   60 * time.Second,
		RecordCmd:      "ffmpeg -i {rtsp_url} -c copy {output}",
		CamOverrides: map[string]config.CamOverride{
			"BACKYARD": {RecordCmd: &perCam},
		},
	}
	m := NewManager(cfg, nil, zerolog.Nop())

	_, args := m.buildFFmpegArgs("backyard")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "h264_nvenc") {
		t.Errorf("per-camera override didn't win: %s", joined)
	}
	if strings.Contains(joined, "-c copy") {
		t.Errorf("global override leaked through: %s", joined)
	}
}

// TestBuildFFmpegArgs_BadTemplateFallsBackWithIssue pins the
// resilience contract: a malformed RECORD_CMD doesn't disable
// recording, it just reports and falls back to the built-in argv.
func TestBuildFFmpegArgs_BadTemplateFallsBackWithIssue(t *testing.T) {
	cfg := &config.Config{
		// valid time-template path so it doesn't emit its own issue
		RecordPath:     "/tmp/rec/{cam_name}/%Y-%m-%d",
		RecordFileName: "%H-%M-%S",
		RecordLength:   60 * time.Second,
		RecordCmd:      "ffmpeg -i {rtsp_url} -tag {typo_here} {output}",
	}
	reg := issues.New()
	m := NewManager(cfg, reg, zerolog.Nop())

	_, args := m.buildFFmpegArgs("cam")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Errorf("expected fallback to default -c:v copy argv; got: %s", joined)
	}
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(list), list)
	}
	if !strings.Contains(list[0].Detail, "{typo_here}") {
		t.Errorf("issue detail should mention the bad token: %+v", list[0])
	}
}

// TestStart_AlwaysSpawnsSupervisor verifies that Start creates the
// supervisor entry regardless of IsEnabled — the auto-start callback
// in wireCameraStateChanges gates on IsEnabled before calling in, but
// a direct Start call (e.g. from the manual record-button click)
// must always honor the request. Spawning ffmpeg fails in this test
// environment, but that's supervised by the backoff loop; what we
// care about here is the registry entry appearing.
func TestStart_AlwaysSpawnsSupervisor(t *testing.T) {
	cfg := &config.Config{RecordAll: false}
	m := NewManager(cfg, nil, zerolog.Nop())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := m.Start(ctx, "any_cam"); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	m.mu.Lock()
	n := len(m.recorders)
	m.mu.Unlock()
	if n != 1 {
		t.Errorf("expected 1 supervisor running after manual Start, got %d", n)
	}
	m.Shutdown()
}
