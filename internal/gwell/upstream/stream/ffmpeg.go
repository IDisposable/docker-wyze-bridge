package stream

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// FFmpegPublisher pipes raw H.264 Annex B data to ffmpeg, which publishes
// the stream via RTSP PUSH (ANNOUNCE/RECORD). The target is any RTSP
// server that accepts publishes — upstream targets mediamtx; our bridge
// points it at go2rtc on loopback. Both speak the same RTSP dialect.
type FFmpegPublisher struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	done  chan struct{}
}

// StartFFmpegPublisher spawns an ffmpeg process that reads raw H.264 from
// stdin and publishes it via RTSP PUSH to rtsp://<rtspHost>:<rtspPort>/<streamPath>.
func StartFFmpegPublisher(streamPath, rtspHost string, rtspPort int) (*FFmpegPublisher, error) {
	rtspURL := fmt.Sprintf("rtsp://%s:%d/%s", rtspHost, rtspPort, streamPath)
	log.Printf("[ffmpeg] Publishing to %s", rtspURL)

	// Timestamp dance for raw H.264 Annex B → RTSP PUSH, go2rtc-compatible:
	//
	//   -use_wallclock_as_timestamps 1  — initial PTS source since
	//     raw H.264 has no container timestamps. Alone, this produces
	//     wallclock-based 90kHz RTP ticks that go2rtc rejects.
	//   -fflags +genpts+igndts          — let ffmpeg's bitstream layer
	//     synthesize monotonic PTS (ignoring DTS which is also absent);
	//     works in concert with +use_wallclock_as_timestamps rather than
	//     as a replacement.
	//   -r 15                           — input framerate hint.
	//     Doorbell Pro encodes at ~15fps; without this ffmpeg can't
	//     compute frame duration for the RTP muxer and emits
	//     "Timestamps are unset in a packet" errors. 15 is a safe
	//     default for GW_* cameras; variable-rate streams still work
	//     because the RTP timestamps stay monotonic.
	//   -bsf:v dump_extra               — force SPS/PPS to appear
	//     in-band in the first keyframe so go2rtc can build the SDP
	//     without a separate sprop-parameter-sets negotiation.
	//   -avoid_negative_ts make_zero    — rebase at stream start so
	//     the RTP sender's first packet has timestamp 0 rather than
	//     a gigantic wallclock-derived value that looks like a rollover
	//     to consumers.
	// Re-encode path chosen deliberately over the -c:v copy pass-through:
	//
	// Empirically, go2rtc's RTSP server closes our publish on the very
	// first RTP data packet when we copy the gwell stream through.
	// Debug output showed ffmpeg's input start_time resolving to raw
	// wallclock microseconds (~2×10¹⁵), with first_dts values diverging
	// between AVPackets in the input queue. Every timestamp-rebase
	// option we tried (-copyts, -start_at_zero, -avoid_negative_ts,
	// +genpts, +igndts) operates output-side, AFTER the input queue's
	// confusion is already encoded into the first RTP timestamp.
	//
	// Re-encoding with libx264 ultrafast decodes the camera stream,
	// discards its wallclock-origin timestamps entirely, and emits
	// fresh monotonic PTS/DTS starting at 0. The RTP muxer then
	// ticks 90kHz forward from zero — the shape every RTSP server
	// expects and the shape go2rtc accepts.
	//
	// Cost: ~10-15% of one CPU core per 1440×1440@15fps camera with
	// ultrafast+zerolatency. -g 30 gives a keyframe every 2s so new
	// consumers can join quickly without a stale-reference wait.
	//
	// Loglevel stays at `debug` for one more run to confirm the full
	// RTSP handshake completes and frames flow; dial back to `warning`
	// once we've seen it work.
	cmd := exec.Command("ffmpeg",
		"-loglevel", "debug",
		"-use_wallclock_as_timestamps", "1",
		"-fflags", "+genpts+igndts+nobuffer",
		"-r", "15",
		"-f", "h264",
		"-i", "pipe:0",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-g", "30",
		"-pix_fmt", "yuv420p",
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		rtspURL,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin pipe: %w", err)
	}

	// Send ffmpeg stderr to our log
	cmd.Stderr = &logWriter{prefix: "[ffmpeg]"}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	p := &FFmpegPublisher{
		cmd:   cmd,
		stdin: stdin,
		done:  make(chan struct{}),
	}

	go func() {
		err := cmd.Wait()
		log.Printf("[ffmpeg] Process exited: %v", err)
		close(p.done)
	}()

	log.Printf("[ffmpeg] Started (pid %d)", cmd.Process.Pid)
	return p, nil
}

// Write sends raw H.264 Annex B data to ffmpeg's stdin.
func (p *FFmpegPublisher) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	select {
	case <-p.done:
		return 0, fmt.Errorf("ffmpeg process has exited")
	default:
	}

	return p.stdin.Write(data)
}

// Alive returns true if the ffmpeg process is still running.
func (p *FFmpegPublisher) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Close terminates the ffmpeg process.
func (p *FFmpegPublisher) Close() error {
	p.stdin.Close()
	select {
	case <-p.done:
		return nil
	default:
		return p.cmd.Process.Kill()
	}
}

// logWriter adapts log.Printf to io.Writer for ffmpeg stderr.
type logWriter struct {
	prefix string
}

func (w *logWriter) Write(p []byte) (int, error) {
	s := string(p)
	// Suppress DTS timestamp spam — ffmpeg handles it automatically
	if strings.Contains(s, "Non-monotonic DTS") || strings.Contains(s, "changing to") {
		return len(p), nil
	}
	log.Printf("%s %s", w.prefix, s)
	return len(p), nil
}
