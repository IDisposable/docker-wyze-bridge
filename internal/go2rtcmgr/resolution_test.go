package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
	"time"
)

// Opt-in integration check against a local replay or an existing native feed.
func TestProbeDecodedResolutionRTSP(t *testing.T) {
	source := os.Getenv("WYZE_TEST_NATIVE_RTSP")
	if source == "" {
		t.Skip("set WYZE_TEST_NATIVE_RTSP for the bounded decoder integration check")
	}
	wantWidth, _ := strconv.Atoi(os.Getenv("WYZE_TEST_NATIVE_WIDTH"))
	wantHeight, _ := strconv.Atoi(os.Getenv("WYZE_TEST_NATIVE_HEIGHT"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	w, h, err := ProbeDecodedResolution(ctx, source)
	if err != nil || w != wantWidth || h != wantHeight {
		t.Fatalf("decoded %dx%d: %v", w, h, err)
	}
	t.Logf("decoded %dx%d", w, h)
}

func TestDecodedResolutionIgnoresStaleMetadata(t *testing.T) {
	// This is the observed failure: the stream header says 4K, each decoded
	// frame says 360p. The guard must see 360p rather than the header.
	w, h, err := decodedResolution(resolutionSample(func(int) (int, int) { return 640, 360 }))
	if err != nil || w != 640 || h != 360 {
		t.Fatalf("got %dx%d, %v", w, h, err)
	}
}

func TestDecodedResolutionRejectsUncertainSamples(t *testing.T) {
	for _, raw := range []string{`{}`, `invalid`, `{"frames":[{"width":640,"height":360}]}`, `{"frames":[{},{},{}]}`, `{"frames":[{"width":640,"height":360},{"width":3840,"height":2160},{"width":3840,"height":2160}]}`} {
		if _, _, err := decodedResolution([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if w, h, err := decodedResolution(resolutionSample(func(int) (int, int) { return 3840, 2160 })); err != nil || w != 3840 || h != 2160 {
		t.Fatalf("healthy 4K: %dx%d %v", w, h, err)
	}
}

func resolutionSample(size func(int) (int, int)) []byte {
	type frame struct {
		Width, Height int
		PTS           string `json:"best_effort_timestamp_time"`
	}
	frames := make([]frame, 240)
	for i := range frames {
		w, h := size(i)
		frames[i] = frame{w, h, fmt.Sprintf("%.3f", 100+float64(i)/20)}
	}
	raw, _ := json.Marshal(map[string]any{"streams": []map[string]int{{"width": 3840, "height": 2160}}, "frames": frames})
	return raw
}

func TestDecodedResolutionMixedQualityRegression(t *testing.T) {
	for _, tc := range []struct {
		name          string
		size          func(int) (int, int)
		width, height int
	}{
		{"4k-spikes-in-360p", func(i int) (int, int) {
			if i%20 == 0 {
				return 3840, 2160
			}
			return 640, 360
		}, 640, 360},
		{"observed-high-lead-then-low", func(i int) (int, int) {
			if i < 29 {
				return 3840, 2160
			}
			return 640, 360
		}, 640, 360},
		{"stale-low-startup", func(i int) (int, int) {
			if i < 60 {
				return 640, 360
			}
			return 3840, 2160
		}, 3840, 2160},
		{"recovered-tail", func(i int) (int, int) {
			if i < 190 {
				return 640, 360
			}
			return 3840, 2160
		}, 3840, 2160},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h, err := decodedResolution(resolutionSample(tc.size))
			if err != nil || w != tc.width || h != tc.height {
				t.Fatalf("decoded %dx%d: %v", w, h, err)
			}
		})
	}
}

func TestDecodedResolutionRejectsBadTiming(t *testing.T) {
	for _, raw := range []string{
		`{"frames":[{"Width":640,"Height":360,"best_effort_timestamp_time":"NaN"}]}`,
		`{"frames":[{"Width":640,"Height":360,"best_effort_timestamp_time":"+Inf"}]}`,
		`{"frames":[{"Width":640,"Height":360,"best_effort_timestamp_time":"2"},{"Width":640,"Height":360,"best_effort_timestamp_time":"1"}]}`,
	} {
		if _, _, err := decodedResolution([]byte(raw)); err == nil {
			t.Fatal("accepted bad/insufficient timestamps")
		}
	}
}

func TestProbeOutputLimitIncludesPipeCopy(t *testing.T) {
	var output probeOutput
	_, err := io.Copy(&output, io.LimitReader(bytes.NewReader(make([]byte, 64*1024+1)), 64*1024+1))
	if err == nil || output.Len() > 64*1024 {
		t.Fatal("subprocess pipe bypassed the output limit")
	}
}
