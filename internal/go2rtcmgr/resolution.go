package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os/exec"
	"strconv"
	"time"
)

// Embedding bytes.Buffer would promote ReadFrom and let os/exec's io.Copy
// bypass Write's output limit.
type probeOutput struct{ buffer bytes.Buffer }

func (b *probeOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *probeOutput) Len() int      { return b.buffer.Len() }

func (b *probeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, errors.New("frame sample exceeds limit")
	}
	return b.buffer.Write(p)
}

// ProbeDecodedResolution uses the image's existing ffprobe, briefly decoding
// native frames with two threads. Container/SDP width can remain 4K after the
// camera switches to 360p, so only per-frame dimensions are authoritative.
// Callers must supply a timeout and only sample an already-active local feed.
func ProbeDecodedResolution(ctx context.Context, source string) (int, int, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-threads", "2",
		"-rtsp_transport", "tcp", "-timeout", "8000000", "-analyzeduration", "3000000",
		"-probesize", "1000000", "-read_intervals", "%+12", "-select_streams", "v:0",
		"-show_frames", "-show_entries", "frame=width,height,best_effort_timestamp_time", "-of", "json", source)
	var output probeOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard // URLs/errors may contain credentials.
	cmd.WaitDelay = 2 * time.Second
	if cmd.Run() != nil {
		return 0, 0, errors.New("decoded frame sample unavailable")
	}
	return decodedResolution(output.Bytes())
}

func decodedResolution(raw []byte) (int, int, error) {
	var sample struct {
		Frames []struct {
			Width, Height int
			PTS           string `json:"best_effort_timestamp_time"`
		}
	}
	if len(raw) > 64*1024 || json.Unmarshal(raw, &sample) != nil {
		return 0, 0, errors.New("insufficient decoded frames")
	}
	type frame struct {
		width, height int
		pts           float64
	}
	var steady []frame
	var first, previous float64
	started := false
	for _, f := range sample.Frames {
		if f.Width <= 0 || f.Height <= 0 {
			return 0, 0, errors.New("invalid frame dimensions")
		}
		pts, err := strconv.ParseFloat(f.PTS, 64)
		if err != nil || math.IsNaN(pts) || math.IsInf(pts, 0) {
			continue
		}
		if !started {
			first, previous, started = pts, pts, true
		}
		if pts < previous {
			return 0, 0, errors.New("nonmonotonic frame sample")
		}
		previous = pts
		// New decoders initially expose cached parameters until a fresh keyframe.
		if pts-first >= 3 {
			steady = append(steady, frame{f.Width, f.Height, pts - first})
		}
	}
	if len(steady) < 30 || steady[len(steady)-1].pts-steady[0].pts < 6 {
		return 0, 0, errors.New("insufficient steady decoded frames")
	}
	var low, tail, tailLow, lowW, lowH, highW, highH int
	end := steady[len(steady)-1].pts
	for _, f := range steady {
		isLow := f.width < 3840 || f.height < 2160
		if isLow {
			low++
			lowW, lowH = f.width, f.height
		} else {
			highW, highH = f.width, f.height
		}
		if f.pts >= end-2 {
			tail++
			if isLow {
				tailLow++
			}
		}
	}
	// A few 4K frames must not hide a mostly-low stream that remains low.
	if 2*low >= len(steady) && 2*tailLow >= tail {
		return lowW, lowH, nil
	}
	// A sustained healthy tail means the source has already recovered.
	if 2*tailLow < tail && highW > 0 {
		return highW, highH, nil
	}
	return 0, 0, errors.New("resolution transition in sample")
}
