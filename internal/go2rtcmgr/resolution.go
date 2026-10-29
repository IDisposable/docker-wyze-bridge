package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
)

type probeOutput struct{ bytes.Buffer }

func (b *probeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, errors.New("frame sample exceeds limit")
	}
	return b.Buffer.Write(p)
}

// ProbeDecodedResolution uses the image's existing ffprobe, briefly decoding
// native frames with two threads. Container/SDP width can remain 4K after the
// camera switches to 360p, so only per-frame dimensions are authoritative.
// Callers must supply a timeout and only sample an already-active local feed.
func ProbeDecodedResolution(ctx context.Context, source string) (int, int, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-threads", "2",
		"-rtsp_transport", "tcp", "-timeout", "8000000", "-analyzeduration", "3000000",
		"-probesize", "1000000", "-read_intervals", "%+1", "-select_streams", "v:0",
		"-show_frames", "-show_entries", "frame=width,height", "-of", "json", source)
	var output probeOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard // URLs/errors may contain credentials.
	if cmd.Run() != nil {
		return 0, 0, errors.New("decoded frame sample unavailable")
	}
	return decodedResolution(output.Bytes())
}

func decodedResolution(raw []byte) (int, int, error) {
	var sample struct{ Frames []struct{ Width, Height int } }
	if json.Unmarshal(raw, &sample) != nil || len(sample.Frames) < 3 {
		return 0, 0, errors.New("insufficient decoded frames")
	}
	w, h := sample.Frames[0].Width, sample.Frames[0].Height
	if w <= 0 || h <= 0 {
		return 0, 0, errors.New("invalid frame dimensions")
	}
	for _, f := range sample.Frames {
		if f.Width != w || f.Height != h {
			return 0, 0, errors.New("resolution transition in sample")
		}
	}
	return w, h, nil
}
