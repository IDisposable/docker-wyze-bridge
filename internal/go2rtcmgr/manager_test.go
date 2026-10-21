package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestManager_NewManager(t *testing.T) {
	m := NewManager("/usr/local/bin/go2rtc", "/config/go2rtc.yaml", zerolog.Nop())

	if m.binaryPath != "/usr/local/bin/go2rtc" {
		t.Errorf("binaryPath = %q", m.binaryPath)
	}
	if m.configPath != "/config/go2rtc.yaml" {
		t.Errorf("configPath = %q", m.configPath)
	}
	if m.APIURL() != "http://127.0.0.1:1984" {
		t.Errorf("APIURL = %q", m.APIURL())
	}
}

func TestManager_IsHealthy_NoServer(t *testing.T) {
	m := NewManager("", "", zerolog.Nop())
	m.apiURL = "http://localhost:19998" // unlikely to be listening

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if m.IsHealthy(ctx) {
		t.Error("should not be healthy with no server")
	}
}

func TestManager_StartMissingBinary(t *testing.T) {
	m := NewManager("/nonexistent/binary", "/tmp/test.yaml", zerolog.Nop())

	err := m.Start(context.Background())
	if err == nil {
		t.Error("expected error starting missing binary")
	}
}

func TestEmitLogLine(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantLevel string // zerolog JSON "level" value; "" = no output expected
		wantMsg   string // expected logged message (prefix stripped for parsed lines)
	}{
		{name: "empty", line: "", wantLevel: ""},
		{name: "whitespace", line: "   ", wantLevel: ""},
		{name: "zerolog DBG demoted to trace",
			line:      "21:38:15.743 DBG [streams] start producer url=wyze://x",
			wantLevel: "trace", wantMsg: "[streams] start producer url=wyze://x"},
		{name: "zerolog TRC stays trace",
			line:      "21:38:15.743 TRC some trace",
			wantLevel: "trace", wantMsg: "some trace"},
		{name: "zerolog INF demoted to debug",
			line:      "21:36:44.606 INF go2rtc platform=linux/amd64",
			wantLevel: "debug", wantMsg: "go2rtc platform=linux/amd64"},
		{name: "zerolog WRN stays warn",
			line:      "21:36:44.606 WRN something odd",
			wantLevel: "warn", wantMsg: "something odd"},
		{name: "zerolog ERR stays error",
			line:      "21:38:51.720 ERR streams: wyze: connect failed",
			wantLevel: "error", wantMsg: "streams: wyze: connect failed"},
		{name: "zerolog FTL demoted to error (keep bridge alive)",
			line:      "21:38:51.720 FTL fatal thing",
			wantLevel: "error", wantMsg: "fatal thing"},
		{name: "unprefixed [OOO] reset noise suppressed",
			line:      "[OOO] ch=0x05 #3643 frameType=0x00 pktTotal=107 expected pkt 0, got 102 - reset",
			wantLevel: ""},
		{name: "unknown level token → trace",
			line:      "12:00:00.000 ??? weird line",
			wantLevel: "trace", wantMsg: "12:00:00.000 ??? weird line"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			m := NewManager("", "", zerolog.New(&buf).Level(zerolog.TraceLevel))
			m.emitLogLine(tc.line)

			out := strings.TrimSpace(buf.String())
			if tc.wantLevel == "" {
				if out != "" {
					t.Errorf("expected no log output, got %q", out)
				}
				return
			}
			var entry map[string]interface{}
			if err := json.Unmarshal([]byte(out), &entry); err != nil {
				t.Fatalf("unmarshal log %q: %v", out, err)
			}
			if got, _ := entry["level"].(string); got != tc.wantLevel {
				t.Errorf("level = %q, want %q", got, tc.wantLevel)
			}
			if got, _ := entry["message"].(string); got != tc.wantMsg {
				t.Errorf("message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

func TestParseStreamError(t *testing.T) {
	// go2rtc only reports a failed camera dial in its log, so the bridge
	// pulls the stream name and error text back out of the line. Lines
	// with no stream= value can't be tied to a camera and are skipped.
	cases := []struct {
		in          string
		stream, err string
		ok          bool
	}{
		{`[rtsp] error="streams: wyze: connect failed: discovery timeout" stream=side_garage`, "side_garage", "wyze: connect failed: discovery timeout", true},
		{`[rtsp] error="streams: websocket: close 1006 (abnormal closure): unexpected EOF" stream=front_door`, "front_door", "websocket: close 1006 (abnormal closure): unexpected EOF", true},
		{`[streams] stream=garage_cam something odd happened`, "garage_cam", "[streams]  something odd happened", true},
		{`github.com/AlexxIT/go2rtc/internal/mjpeg/mjpeg.go:82 > error="streams: wyze: connect failed: discovery timeout"`, "", "", false},
		{`stream=`, "", "", false},
		{``, "", "", false},
	}
	for _, c := range cases {
		stream, errText, ok := ParseStreamError(c.in)
		if ok != c.ok || stream != c.stream || errText != c.err {
			t.Errorf("ParseStreamError(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, stream, errText, ok, c.stream, c.err, c.ok)
		}
	}
}

func TestEmitLogLine_WarningObserver(t *testing.T) {
	// Only go2rtc's WRN and ERR lines reach the warning observer, with the
	// level spelled out. Info lines, known noise and lines with no level
	// are not passed on.
	m := NewManager("go2rtc", "/tmp/x.yaml", zerolog.Nop())
	var got []string
	m.OnWarning(func(level, msg string) { got = append(got, level+": "+msg) })
	m.emitLogLine(`12:00:00.000 WRN [rtsp] error="streams: wyze: connect failed: discovery timeout" stream=side_garage`)
	m.emitLogLine(`12:00:00.000 ERR [api] boom`)
	m.emitLogLine(`12:00:00.000 INF [api] listen addr=:1984`) // not a warning
	m.emitLogLine(`[OOO] something - reset`)                  // suppressed noise
	m.emitLogLine(`garbage without a level`)
	want := []string{
		`warn: [rtsp] error="streams: wyze: connect failed: discovery timeout" stream=side_garage`,
		`error: [api] boom`,
	}
	if len(got) != len(want) {
		t.Fatalf("observer got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProducerInfo_ConnectedAndBytes(t *testing.T) {
	// A producer that only has a URL isn't connected. Bytes come from the
	// receivers or from bytes_recv, and a stream adds up all its producers
	// so the health check sees the real flow.
	idle := ProducerInfo{URL: "wyze://10.0.0.1"}
	if idle.Connected() || idle.Bytes() != 0 {
		t.Error("a bare url producer is not connected")
	}
	live := ProducerInfo{URL: "wyze://10.0.0.1", RemoteAddr: "10.0.0.1:32761", Receivers: []ReceiverInfo{{Bytes: 1000, Packets: 5}, {Bytes: 24}}}
	if !live.Connected() || live.Bytes() != 1024 {
		t.Errorf("live: connected=%v bytes=%d", live.Connected(), live.Bytes())
	}
	rtsp := ProducerInfo{URL: "rtsp://x", BytesRecv: 77}
	if !rtsp.Connected() || rtsp.Bytes() != 77 {
		t.Errorf("rtsp: connected=%v bytes=%d", rtsp.Connected(), rtsp.Bytes())
	}
	s := &StreamInfo{Producers: []ProducerInfo{idle, live, rtsp}}
	if !s.ProducerConnected() || s.ProducerBytes() != 1101 {
		t.Errorf("stream: connected=%v bytes=%d", s.ProducerConnected(), s.ProducerBytes())
	}
	empty := &StreamInfo{}
	if empty.ProducerConnected() || empty.ProducerBytes() != 0 {
		t.Error("empty stream should be disconnected with zero bytes")
	}
}

func TestListStreams_ParsesProducerCounters(t *testing.T) {
	// ListStreams decodes go2rtc's /api/streams JSON into producer and
	// consumer counters, so a live camera shows its bytes and an idle one
	// with just a URL reads as disconnected.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"side_garage":{"producers":[{"id":3,"format_name":"wyze","protocol":"udp","remote_addr":"192.168.4.151:32761","url":"wyze://192.168.4.151?uid=x","medias":["video, recvonly, H264"],"receivers":[{"id":4,"codec":{"codec_name":"h264"},"bytes":123456,"packets":321}]}],"consumers":[{"id":9,"format_name":"rtsp","bytes_send":5}]},"idle_cam":{"producers":[{"url":"wyze://10.0.0.2"}],"consumers":[]}}`))
	}))
	defer srv.Close()
	c := NewAPIClient(srv.URL, zerolog.Nop())
	streams, err := c.ListStreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg := streams["side_garage"]
	if sg == nil || !sg.ProducerConnected() || sg.ProducerBytes() != 123456 || len(sg.Consumers) != 1 {
		t.Errorf("side_garage = %+v", sg)
	}
	idle := streams["idle_cam"]
	if idle == nil || idle.ProducerConnected() || idle.ProducerBytes() != 0 || len(idle.Consumers) != 0 {
		t.Errorf("idle_cam = %+v", idle)
	}
}
