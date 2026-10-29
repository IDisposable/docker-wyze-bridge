package go2rtcmgr

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	w, h, err := decodedResolution([]byte(`{"streams":[{"width":3840,"height":2160}],"frames":[{"width":640,"height":360},{"width":640,"height":360},{"width":640,"height":360}]}`))
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
	if w, h, err := decodedResolution([]byte(`{"frames":[{"width":3840,"height":2160},{"width":3840,"height":2160},{"width":3840,"height":2160}]}`)); err != nil || w != 3840 || h != 2160 {
		t.Fatalf("healthy 4K: %dx%d %v", w, h, err)
	}
}
