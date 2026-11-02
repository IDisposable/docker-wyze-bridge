package go2rtcmgr

import (
	"encoding/json"
	"testing"
)

// Producer shapes go2rtc 1.9.14 reports, copied off a running bridge.
// A connected WebRTC producer carries "url" and a producer-level
// bytes_recv that lumps audio and video together; a connected TUTK
// producer carries "source" and no bytes_recv at all. Both tag their
// receivers with codec.codec_type. A stream go2rtc never managed to
// dial is a bare url with nothing else.

const webrtcProducerJSON = `{
  "name": "front_door",
  "producers": [{
    "format_name": "webrtc/wyze",
    "remote_addr": "192.168.4.157:35698 host",
    "url": "wss://wyze-mars-webcsrv.wyzecam.com?x=1",
    "receivers": [
      {"id":2684,"codec":{"codec_name":"h264","codec_type":"video"},"bytes":35671751,"packets":64539},
      {"id":2686,"codec":{"codec_name":"pcm_mulaw","codec_type":"audio","sample_rate":8000},"bytes":16385440,"packets":102409}
    ],
    "bytes_recv": 54058074
  }]
}`

const tutkProducerJSON = `{
  "name": "side_yard",
  "producers": [{
    "format_name": "wyze",
    "remote_addr": "192.168.4.139:58345",
    "source": "wyze://192.168.4.139?x=1",
    "receivers": [
      {"id":1125,"codec":{"codec_name":"h264","codec_type":"video"},"bytes":616819515,"packets":588120},
      {"id":2749,"codec":{"codec_name":"pcm_s16le","codec_type":"audio","sample_rate":8000},"bytes":23879680,"packets":37312}
    ]
  }]
}`

const undialedProducerJSON = `{
  "name": "driveway_bedb",
  "producers": [{"url": "wyze://192.168.4.141?x=1"}]
}`

func parseStream(t *testing.T, raw string) *StreamInfo {
	t.Helper()
	var s StreamInfo
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &s
}

func TestSourceURL_ReadsEitherKey(t *testing.T) {
	if got := parseStream(t, webrtcProducerJSON).Producers[0].SourceURL(); got == "" {
		t.Error(`WebRTC producer's "url" not read`)
	}
	if got := parseStream(t, tutkProducerJSON).Producers[0].SourceURL(); got != "wyze://192.168.4.139?x=1" {
		t.Errorf(`TUTK producer's "source" read as %q`, got)
	}
}

func TestProducerConnected_UndialedIsNotConnected(t *testing.T) {
	s := parseStream(t, undialedProducerJSON)
	if len(s.Producers) != 1 {
		t.Fatalf("producers = %d, want 1: the entry exists, which is why a producers==0 check never catches it", len(s.Producers))
	}
	if s.ProducerConnected() {
		t.Error("a bare {url} producer must not count as connected")
	}
}

func TestVideoBytes_CountsTheVideoTrackOnly(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		want      int
		wantKnown bool
		wantVideo bool
	}{
		{"webrtc", webrtcProducerJSON, 35671751, true, true},
		{"tutk", tutkProducerJSON, 616819515, true, true},
		{"undialed", undialedProducerJSON, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseStream(t, tt.raw)
			got, known := s.VideoBytes()
			if got != tt.want || known != tt.wantKnown {
				t.Errorf("VideoBytes() = (%d, %v), want (%d, %v)", got, known, tt.want, tt.wantKnown)
			}
			if gotVideo := s.HasVideoReceiver(); gotVideo != tt.wantVideo {
				t.Errorf("HasVideoReceiver() = %v, want %v", gotVideo, tt.wantVideo)
			}
		})
	}
}

func TestVideoBytes_IgnoresAudioOnlyFlow(t *testing.T) {
	// A camera whose video stopped while audio kept arriving used to
	// read as live, because the producer total still moved.
	const raw = `{"name":"x","producers":[{"remote_addr":"1.2.3.4:5","receivers":[
		{"codec":{"codec_type":"video"},"bytes":1000},
		{"codec":{"codec_type":"audio"},"bytes":5000}]}]}`
	s := parseStream(t, raw)
	got, known := s.VideoBytes()
	if !known || got != 1000 {
		t.Fatalf("VideoBytes() = (%d, %v), want (1000, true)", got, known)
	}
	if s.ProducerBytes() == got {
		t.Error("ProducerBytes should still include audio; the two must differ here")
	}
}

func TestVideoBytes_UnknownWithoutCodecTypes(t *testing.T) {
	// Receivers with no codec block (an older go2rtc). Reading that
	// zero as no video would restart every working camera, so known
	// must be false and callers fall back to ProducerBytes.
	const raw = `{"name":"x","producers":[{"remote_addr":"1.2.3.4:5","bytes_recv":4096,
		"receivers":[{"bytes":2048,"packets":9}]}]}`
	s := parseStream(t, raw)
	if got, known := s.VideoBytes(); known {
		t.Errorf("VideoBytes() = (%d, true), want known=false without codec types", got)
	}
	if got := s.ProducerBytes(); got != 4096+2048 {
		t.Errorf("ProducerBytes() = %d, want %d", got, 4096+2048)
	}
}
