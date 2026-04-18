package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
)

// spawnGwellProxy runs the gwell-proxy subprocess for the lifetime of
// ctx, restarting it on unexpected exit. The proxy discovers Gwell
// cameras via our /internal/wyze shim and RTSP-PUSHes their H.264 to
// go2rtc. If ctx is cancelled (shutdown), the subprocess dies with us.
//
// No-op design: if cfg.GwellBinary is set, use it; otherwise look in
// PATH for "gwell-proxy". If nothing found, log once and return —
// there's no value in spamming reconnect attempts when the binary is
// simply not shipped (e.g. during local dev before a container rebuild).
func spawnGwellProxy(ctx context.Context, cfg *config.Config, log zerolog.Logger) {
	bin := cfg.GwellBinary
	if bin == "" {
		if resolved, err := exec.LookPath("gwell-proxy"); err == nil {
			bin = resolved
		} else {
			// Fallback: the Docker image installs at a fixed path; try it
			// before giving up so users running without the PATH entry
			// (e.g. minimal systemd envs) still work.
			for _, candidate := range []string{"/usr/local/bin/gwell-proxy", "./gwell-proxy", "./gwell-proxy.exe"} {
				if _, err := os.Stat(candidate); err == nil {
					bin = candidate
					break
				}
			}
		}
	}
	if bin == "" {
		log.Warn().Msg("gwell-proxy binary not found; GW_ cameras will not stream. Set GWELL_BINARY or put gwell-proxy on PATH.")
		return
	}

	shimURL := fmt.Sprintf("http://127.0.0.1:%d/internal/wyze", cfg.BridgePort)
	// The proxy publishes RTSP to whatever go2rtc this bridge manages.
	// In embedded mode that's go2rtc on loopback :8554; in external
	// mode (GO2RTC_URL set) it's the remote host. We only run gwell-proxy
	// in embedded mode for now — external mode means recording/auth are
	// the remote's problem, and dragging a Gwell subprocess into that
	// setup isn't clearly useful. Documented as a limitation.
	rtspHost := "127.0.0.1"
	rtspPort := 8554
	if cfg.Go2RTCURL != "" {
		log.Warn().
			Str("url", cfg.Go2RTCURL).
			Msg("GO2RTC_URL is set; gwell-proxy will still publish to 127.0.0.1:8554 — configure the remote go2rtc to ingest if you need Gwell cameras there")
	}

	// Diagnostic knobs (GWELL_DUMP_DIR + GWELL_FFMPEG_LOGLEVEL). Both
	// live on Config so the values are visible in a single place rather
	// than scattered env lookups. Defaults: no dump, ffmpeg at warning
	// — debug produces thousands of lines/sec that drown out everything
	// else in the bridge log.
	dumpDir := cfg.GwellDumpDir
	ffmpegLogLevel := cfg.GwellFFmpegLogLevel
	deadmanTimeout := cfg.GwellDeadmanTimeout

	backoff := 2 * time.Second
	const maxBackoff = 60 * time.Second

	for ctx.Err() == nil {
		if err := runGwellProxyOnce(ctx, bin, shimURL, rtspHost, rtspPort, dumpDir, ffmpegLogLevel, deadmanTimeout, log); err != nil {
			log.Warn().Err(err).Dur("backoff", backoff).Msg("gwell-proxy exited; restarting")
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

// runGwellProxyOnce starts gwell-proxy and blocks until it exits or
// the parent context cancels. Stdout/stderr are routed through the
// caller's zerolog logger so the subprocess noise blends into the
// bridge's structured log output.
func runGwellProxyOnce(ctx context.Context, bin, shimURL, rtspHost string, rtspPort int, dumpDir, ffmpegLogLevel string, deadmanTimeout time.Duration, log zerolog.Logger) error {
	args := []string{
		"--shim-url", shimURL,
		"--rtsp-host", rtspHost,
		"--rtsp-port", strconv.Itoa(rtspPort),
	}
	if dumpDir != "" {
		args = append(args, "--dump-h264", dumpDir)
	}
	if ffmpegLogLevel != "" {
		args = append(args, "--ffmpeg-loglevel", ffmpegLogLevel)
	}
	if deadmanTimeout > 0 {
		args = append(args, "--deadman-timeout", deadmanTimeout.String())
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	log.Info().
		Int("pid", cmd.Process.Pid).
		Str("bin", bin).
		Str("shim", shimURL).
		Str("rtsp", fmt.Sprintf("%s:%d", rtspHost, rtspPort)).
		Msg("gwell-proxy started")

	// Drain both pipes into zerolog. The proxy uses the stdlib log
	// package so lines are "YYYY/MM/DD HH:MM:SS.ffffff [prefix] ..."
	// — route everything to Debug; callers who want it louder can
	// flip LOG_LEVEL.
	go relayLines(stdout, log)
	go relayLines(stderr, log)

	return cmd.Wait()
}

func relayLines(r io.Reader, log zerolog.Logger) {
	scanner := bufio.NewScanner(r)
	// Buffer headroom for the occasional long H.264 NAL / diagnostic dump.
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Debug().Str("c", "gwell-proxy").Msg(line)
	}
}
