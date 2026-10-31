package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

const (
	speakMaxUpload  = 10 << 20 // 10 MB
	speakMaxSeconds = 60
)

// speakBusy prevents overlapping playback on one camera speaker.
var speakBusy sync.Map

// handleSpeak plays an uploaded audio clip out of the camera speaker.
//
//	POST /api/cameras/<cam>/speak   multipart field "audio" (any format ffmpeg reads)
//
// The clip is normalised to 16-bit mono PCM (max 60s), a short-lived
// loopback viewer keeps the camera session open, and go2rtc pushes the
// audio into the camera's backchannel. Responds once playback starts.
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request, cam *camera.Camera) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if cam.GetInfo().IsWebRTCStreamer() {
		writeJSONError(w, http.StatusNotImplemented, "voice messages are only available for TUTK cameras")
		return
	}
	if s.cfg.Go2RTCURL != "" {
		writeJSONError(w, http.StatusNotImplemented, "voice messages need the embedded go2rtc (GO2RTC_URL is set)")
		return
	}
	g := s.go2rtc()
	if g == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "bridge still starting; go2rtc not yet ready")
		return
	}
	name := cam.Name()
	if _, busy := speakBusy.LoadOrStore(name, true); busy {
		writeJSONError(w, http.StatusConflict, "a message is already playing on this camera")
		return
	}
	release := func() { speakBusy.Delete(name) }

	r.Body = http.MaxBytesReader(w, r.Body, speakMaxUpload)
	file, _, err := r.FormFile("audio")
	if err != nil {
		release()
		writeJSONError(w, http.StatusBadRequest, "multipart field 'audio' required (max 10 MB)")
		return
	}
	defer file.Close()

	dir, err := os.MkdirTemp("", "wb-speak-")
	if err != nil {
		release()
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cleanup := func() { os.RemoveAll(dir); release() }

	in := filepath.Join(dir, "in")
	if err := saveUpload(file, in); err != nil {
		cleanup()
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := filepath.Join(dir, "msg.wav")
	seconds, err := normaliseClip(r.Context(), in, out)
	if err != nil {
		cleanup()
		writeJSONError(w, http.StatusBadRequest, "could not decode audio: "+err.Error())
		return
	}

	// Keep the camera session alive for the whole clip; go2rtc only holds
	// a Wyze producer open while something consumes the stream.
	keep := time.Duration(seconds*float64(time.Second)) + 15*time.Second
	viewCtx, stopView := context.WithTimeout(s.rootCtx, keep)
	startLoopbackViewer(viewCtx, s.cfg.Go2RTCRTSPPort, s.cfg.StreamAuth, name)
	done := func() { stopView(); cleanup() }

	codec, err := waitBackchannel(r.Context(), g, name, 15*time.Second)
	if err != nil {
		done()
		if errors.Is(err, errNoSpeaker) {
			writeJSONError(w, http.StatusNotImplemented, "this camera does not expose a speaker channel")
		} else {
			writeJSONError(w, http.StatusGatewayTimeout, "camera did not connect: "+err.Error())
		}
		return
	}

	src := fmt.Sprintf("ffmpeg:%s#audio=%s#input=file", out, go2rtcAudioArg(codec))
	if err := g.PlayToStream(r.Context(), name, src); err != nil {
		done()
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.log.Info().Str("cam", name).Float64("seconds", seconds).Str("codec", codec).Msg("voice message playing")
	time.AfterFunc(keep, done)
	writeJSON(w, map[string]interface{}{"status": "ok", "camera": name, "seconds": seconds})
}

func saveUpload(src io.Reader, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("upload failed (max 10 MB): %w", err)
	}
	return nil
}

// normaliseClip decodes any ffmpeg-readable input to 16 kHz mono s16le WAV,
// capped at speakMaxSeconds, and returns its duration in seconds.
func normaliseClip(ctx context.Context, in, out string) (float64, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", in, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		"-t", fmt.Sprint(speakMaxSeconds), out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	st, err := os.Stat(out)
	if err != nil {
		return 0, err
	}
	seconds := float64(st.Size()-44) / 32000 // 16 kHz * 2 bytes
	if seconds < 0.2 {
		return 0, errors.New("clip is empty")
	}
	return seconds, nil
}

func startLoopbackViewer(ctx context.Context, rtspPort int, streamAuth, cam string) {
	if rtspPort == 0 {
		rtspPort = 8554
	}
	host := fmt.Sprintf("127.0.0.1:%d", rtspPort)
	u := url.URL{Scheme: "rtsp", Host: host, Path: "/" + cam}
	if user, pass, ok := strings.Cut(firstStreamAuth(streamAuth), ":"); ok {
		u.User = url.UserPassword(user, pass)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "quiet",
		"-rtsp_transport", "tcp", "-i", u.String(), "-map", "0:v", "-f", "null", "-")
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

// firstStreamAuth returns "user:pass" from STREAM_AUTH, ignoring any
// per-camera scoping ("user:pass@cam1,cam2|user2:pass2").
func firstStreamAuth(raw string) string {
	first, _, _ := strings.Cut(raw, "|")
	first, _, _ = strings.Cut(first, "@")
	return first
}

var errNoSpeaker = errors.New("no speaker channel")

func waitBackchannel(ctx context.Context, g *go2rtcmgr.APIClient, cam string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	sawProducer := false
	for {
		info, err := g.GetStreamInfo(ctx, cam)
		if err == nil && len(info.Producers) > 0 && len(info.Producers[0].Medias) > 0 {
			sawProducer = true
			if codec, _ := g.BackchannelCodec(ctx, cam); codec != "" {
				return codec, nil
			}
		}
		if time.Now().After(deadline) {
			if sawProducer {
				return "", errNoSpeaker
			}
			return "", errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// go2rtcAudioArg maps a go2rtc codec string ("PCML/16000/1") onto the
// ffmpeg source's #audio= argument ("pcml/16000").
func go2rtcAudioArg(codec string) string {
	parts := strings.Split(strings.ToLower(codec), "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0]
	}
	return "pcml/16000"
}
