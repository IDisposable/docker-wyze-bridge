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
// an RTSP stream to mediamtx.
type FFmpegPublisher struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	done  chan struct{}
}

// StartFFmpegPublisher spawns an ffmpeg process that reads raw H.264 from
// stdin and publishes to mediamtx at the given RTSP URL.
func StartFFmpegPublisher(streamPath, mediamtxHost string, mediamtxPort int) (*FFmpegPublisher, error) {
	rtspURL := fmt.Sprintf("rtsp://%s:%d/%s", mediamtxHost, mediamtxPort, streamPath)
	log.Printf("[ffmpeg] Publishing to %s", rtspURL)

	cmd := exec.Command("ffmpeg",
		"-loglevel", "warning",
		"-use_wallclock_as_timestamps", "1",
		"-f", "h264",
		"-i", "pipe:0",
		"-c:v", "copy",
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
