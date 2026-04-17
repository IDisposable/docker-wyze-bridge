package recording

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/issues"
)

// go2rtcRTSPPort is the port our managed go2rtc instance listens on for
// RTSP. Must match internal/go2rtcmgr's RTSPConfig.Listen (":8554").
const go2rtcRTSPPort = 8554

// Start begins continuous MP4 recording for camName. Pulls from go2rtc's
// loopback RTSP endpoint with ffmpeg's segment muxer so we get one file
// per RECORD_LENGTH interval, named from the RECORD_PATH/RECORD_FILE_NAME
// template. No re-encode (`-c copy`) — the bridge stays cheap.
//
// Idempotent: a repeat call for a camera already recording is a no-op.
// Call Stop(camName) to halt, or Shutdown() on bridge exit.
//
// The goroutine that owns the ffmpeg process restarts with exponential
// backoff if ffmpeg exits for any reason other than our context cancel
// (camera briefly offline, go2rtc stream not yet ready, transient IO).
func (m *Manager) Start(ctx context.Context, camName string) error {
	// NOTE: IsEnabled is intentionally NOT checked here. A direct
	// Start call always starts — manual record button clicks must
	// work regardless of RECORD_ALL / RECORD_<CAM> env config. The
	// auto-start-on-StateStreaming path in wireCameraStateChanges
	// does its own IsEnabled gate before calling in.
	m.mu.Lock()
	if _, exists := m.recorders[camName]; exists {
		m.mu.Unlock()
		return nil
	}

	// Fail fast if the recording directory can't be created —
	// permissions problems, bad path, read-only mount. Done
	// synchronously before we spawn the supervisor so the HTTP
	// record-button handler can surface the error instead of the
	// caller seeing "ok" while the background loop quietly loops.
	target, _ := m.buildFFmpegArgs(camName)
	if err := ensureTargetDirs(target, time.Now().UTC()); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("mkdir recording dir: %w", err)
	}

	recCtx, cancel := context.WithCancel(ctx)
	r := &recorder{cancel: cancel, done: make(chan struct{})}
	m.recorders[camName] = r
	cb := m.onChange
	m.mu.Unlock()

	if cb != nil {
		cb(camName, true)
	}

	go func() {
		defer close(r.done)
		m.runRecorder(recCtx, camName)
	}()
	return nil
}

// Stop halts recording for one camera and blocks until ffmpeg has
// exited. Safe to call on a camera that isn't being recorded.
func (m *Manager) Stop(camName string) {
	m.mu.Lock()
	r, ok := m.recorders[camName]
	if ok {
		delete(m.recorders, camName)
	}
	cb := m.onChange
	m.mu.Unlock()
	if !ok {
		return
	}
	r.cancel()
	<-r.done
	if cb != nil {
		cb(camName, false)
	}
}

// Shutdown stops every active recorder. Called from main() on
// bridge shutdown so ffmpeg processes don't outlive the bridge.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	recorders := make(map[string]*recorder, len(m.recorders))
	for name, r := range m.recorders {
		recorders[name] = r
	}
	m.recorders = map[string]*recorder{}
	m.mu.Unlock()

	for _, r := range recorders {
		r.cancel()
	}
	for _, r := range recorders {
		<-r.done
	}
}

// runRecorder is the per-camera ffmpeg supervisor loop. Exits only on
// context cancel (parent shutdown or explicit Stop).
func (m *Manager) runRecorder(ctx context.Context, camName string) {
	backoff := 2 * time.Second
	const maxBackoff = 60 * time.Second

	for ctx.Err() == nil {
		if err := m.runFFmpegOnce(ctx, camName); err != nil && ctx.Err() == nil {
			m.log.Warn().Err(err).Str("cam", camName).Dur("backoff", backoff).Msg("recorder ffmpeg exited; restarting")
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// buildFFmpegArgs constructs the ffmpeg argv for one camera. If the
// user has set RECORD_CMD (or a per-camera RECORD_CMD_<CAM>), that
// template is parsed and expanded; otherwise the built-in default
// argv is used. Template parse errors are reported to the issues
// registry keyed on the camera, and the default argv is used as a
// fallback so a bad template doesn't silently disable recording.
//
// Returns the output target (for MkdirAll on the parent) and the
// argv ready to hand to exec.CommandContext.
func (m *Manager) buildFFmpegArgs(camName string) (target string, args []string) {
	target = m.RecordFileNameForCamera(camName) + ".mp4"

	segmentSec := int(m.cfg.RecordLength.Seconds())
	if segmentSec < 1 {
		segmentSec = 60
	}

	override := m.recordCmdFor(camName)
	if override != "" {
		if tmpl, err := ParseRecordTemplate(override); err == nil {
			m.clearTemplateIssue(camName)
			argv := tmpl.Expand(TemplateContext{
				CamName:    camName,
				Quality:    m.quality(camName),
				RtspHost:   "127.0.0.1",
				RtspPort:   go2rtcRTSPPort,
				Output:     target,
				OutputDir:  filepath.Dir(target),
				OutputStem: strings.TrimSuffix(target, filepath.Ext(target)),
				SegmentSec: segmentSec,
			})
			return target, argv
		} else {
			m.reportTemplateIssue(camName, override, err)
			// fall through to default argv
		}
	}

	rtspURL := fmt.Sprintf("rtsp://127.0.0.1:%d/%s", go2rtcRTSPPort, camName)
	// Video passthrough (H.264 from the camera fits mp4 as-is). Audio
	// transcode to AAC — Wyze TUTK cameras stream PCM s16be and the
	// mp4 muxer won't accept that. AAC re-encode is ~1% of one CPU
	// core per camera and keeps the recording playable everywhere.
	args = []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-c:v", "copy",
		"-c:a", "aac",
		"-b:a", "128k",
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%d", segmentSec),
		"-reset_timestamps", "1",
		"-strftime", "1",
		target,
	}
	return target, args
}

// recordCmdFor returns the effective RECORD_CMD for a camera:
// per-camera override if set, global RECORD_CMD otherwise, empty if
// neither. Empty means "use the built-in default argv".
func (m *Manager) recordCmdFor(camName string) string {
	if ov, ok := m.cfg.CamOverrides[strings.ToUpper(camName)]; ok && ov.RecordCmd != nil {
		return *ov.RecordCmd
	}
	return m.cfg.RecordCmd
}

// quality returns the effective QUALITY setting for a camera.
func (m *Manager) quality(camName string) string {
	if ov, ok := m.cfg.CamOverrides[strings.ToUpper(camName)]; ok && ov.Quality != nil {
		return *ov.Quality
	}
	return m.cfg.Quality
}

func (m *Manager) reportTemplateIssue(camName, raw string, err error) {
	if m.issues == nil {
		return
	}
	m.issues.Report(issues.Issue{
		ID:       "record_cmd/" + camName,
		Severity: issues.SeverityError,
		Scope:    "config",
		Camera:   camName,
		Message:  "RECORD_CMD template failed to parse — using built-in default for this recording",
		Detail:   err.Error(),
		RawValue: raw,
	})
}

func (m *Manager) clearTemplateIssue(camName string) {
	if m.issues == nil {
		return
	}
	m.issues.Resolve("record_cmd/" + camName)
}

// runFFmpegOnce starts ffmpeg, blocks until it exits, returns the exit
// error. Stdout/stderr are relayed into zerolog at debug level so users
// can see recording progress when LOG_LEVEL=debug.
//
// While ffmpeg runs, a ticker re-MkdirAlls both today's and tomorrow's
// strftime-expanded target directory. That way the directory ffmpeg
// needs at a midnight rollover always exists well before it tries to
// open the next segment — no lost segments when the day flips.
func (m *Manager) runFFmpegOnce(ctx context.Context, camName string) error {
	target, args := m.buildFFmpegArgs(camName)

	// Ensure both today's and tomorrow's target directories exist
	// before ffmpeg starts. The segment muxer creates files but not
	// directory trees, and its -strftime expansion happens inside the
	// muxer — so we expand the same tokens ourselves to know which
	// dated subdirectory ffmpeg will try to open next.
	if err := ensureTargetDirs(target, time.Now().UTC()); err != nil {
		return fmt.Errorf("mkdir recording dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	// Pin ffmpeg's -strftime 1 expansion to UTC so its output matches
	// the directory we just mkdir'd with time.Now().UTC(). Without
	// this, a container running with a non-UTC TZ env would have
	// ffmpeg writing to "local-time/%Y/%m/%d" while we created
	// "utc/%Y/%m/%d" — the two agree only in the Z timezone.
	cmd.Env = append(os.Environ(), "TZ=UTC")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	m.log.Info().
		Int("pid", cmd.Process.Pid).
		Str("cam", camName).
		Str("target", target).
		Msg("recording started")

	go m.relayLines(stdout, camName)
	go m.relayLines(stderr, camName)

	// Pump Wait() into a channel so we can select on it alongside the
	// dir-maintenance ticker. Ticking hourly is plenty — MkdirAll is
	// microseconds and idempotent, and any tick within the hour before
	// midnight covers the rollover.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()

	for {
		select {
		case err := <-done:
			if ctx.Err() == nil {
				m.log.Debug().Err(err).Str("cam", camName).Msg("recording ffmpeg exited")
			}
			return err
		case <-tick.C:
			if err := ensureTargetDirs(target, time.Now().UTC()); err != nil {
				m.log.Warn().Err(err).Str("cam", camName).Msg("mkdir recording dir (rollover)")
			}
		}
	}
}

// ensureTargetDirs MkdirAll's today's and tomorrow's strftime-expanded
// parent directory for the given target template. Called before ffmpeg
// starts and once per hour while it runs, so a midnight rollover never
// finds a missing directory.
func ensureTargetDirs(target string, now time.Time) error {
	for _, t := range []time.Time{now, now.Add(24 * time.Hour)} {
		if err := os.MkdirAll(filepath.Dir(expandStrftime(target, t)), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) relayLines(r io.Reader, camName string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m.log.Debug().Str("c", "record-ffmpeg").Str("cam", camName).Msg(line)
	}
}

// recorder is the per-camera supervisor handle kept in Manager.recorders.
type recorder struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// expandStrftime replaces the strftime tokens ffmpeg's -strftime 1 mode
// expands inside the segment muxer, so we can mkdir the same directory
// ffmpeg will open. Only the tokens we document in RECORD_PATH /
// RECORD_FILE_NAME are handled (%Y %m %d %H %M %S %s). Unknown % codes
// pass through untouched so ffmpeg still sees them.
func expandStrftime(path string, t time.Time) string {
	return strings.NewReplacer(
		"%Y", t.Format("2006"),
		"%m", t.Format("01"),
		"%d", t.Format("02"),
		"%H", t.Format("15"),
		"%M", t.Format("04"),
		"%S", t.Format("05"),
		"%s", fmt.Sprintf("%d", t.Unix()),
	).Replace(path)
}

