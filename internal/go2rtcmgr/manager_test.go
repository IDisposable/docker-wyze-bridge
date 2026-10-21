package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

// fakeGo2RTC writes a shell script that stands in for go2rtc: its first
// run prints a Go-style crash to stderr and exits 2, later runs stay up
// until killed. Returns the script path.
func fakeGo2RTC(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := dir + "/go2rtc"
	body := `#!/bin/sh
n=$(cat "` + dir + `/runs" 2>/dev/null || echo 0)
n=$((n+1))
echo $n > "` + dir + `/runs"
if [ $n -eq 1 ]; then
  echo "12:00:00.000 INF [wyze] dialing"
  echo "panic: runtime error: invalid memory address or nil pointer dereference" >&2
  echo "goroutine 42 [running]:" >&2
  echo "github.com/AlexxIT/go2rtc/pkg/wyze.(*Producer).Start(...)" >&2
  exit 2
fi
exec sleep 30
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// syncBuffer is a bytes.Buffer safe for the relay goroutines to write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestManager_RestartsAfterCrash(t *testing.T) {
	// go2rtc panicked on the NAS and the bridge sat there with every
	// camera erroring "connection refused" until the container was
	// restarted by hand. Now: the crash is logged, go2rtc comes back,
	// and OnRestart fires so the streams get added again.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs /bin/sh")
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	}))
	defer api.Close()

	var logs syncBuffer
	m := NewManager(fakeGo2RTC(t), "/dev/null", zerolog.New(&logs))
	m.apiURL = api.URL
	m.restartMin = 10 * time.Millisecond

	restarted := make(chan struct{}, 4)
	m.OnRestart(func() { restarted <- struct{}{} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	m.cancel = cancel
	err := m.spawnLocked(ctx) // Start minus the :1984 pre-flight
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("go2rtc was not restarted; log:\n%s", logs.String())
	}
	out := logs.String()
	for _, want := range []string{
		`"level":"error","message":"panic: runtime error`,
		`"level":"error","message":"goroutine 42 [running]:"`,
		`go2rtc exited unexpectedly`,
		`restarting go2rtc`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s\n%s", want, out)
		}
	}

	// Stop: the running fake is killed and nothing restarts it.
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restarted:
		t.Error("restarted after Stop")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestManager_NextRestartDelay(t *testing.T) {
	// A go2rtc that keeps dying right after start waits twice as long each
	// time, capped at 60s, so a crash loop doesn't spin. After a run that
	// stayed up long enough the wait goes back to the minimum.
	m := NewManager("go2rtc", "/dev/null", zerolog.Nop())
	now := time.Now()
	m.startedAt = now.Add(-time.Second) // died a second after starting
	got := []time.Duration{}
	for range 7 {
		got = append(got, m.nextRestartDelay(now))
	}
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("delays = %v, want %v seconds", got, want)
		}
	}
	// A run that lasted restartStable starts over at restartMin.
	m.startedAt = now.Add(-m.restartStable)
	if d := m.nextRestartDelay(now); d != m.restartMin {
		t.Errorf("after a stable run: %v, want %v", d, m.restartMin)
	}
}

func TestEmitLogLine_CrashDumpBudget(t *testing.T) {
	// Once go2rtc crashes, only crashLineBudget lines of the dump get
	// logged, not every goroutine's stack. Trace-looking lines before the
	// crash starts are dropped.
	var buf bytes.Buffer
	m := NewManager("go2rtc", "/dev/null", zerolog.New(&buf).Level(zerolog.InfoLevel))
	m.emitLogLine("goroutine 1 [running]:") // no crash yet: trace, dropped
	m.emitLogLine("fatal error: concurrent map writes")
	for range crashLineBudget + 10 {
		m.emitLogLine("main.go:1 +0x1")
	}
	if n := strings.Count(buf.String(), `"level":"error"`); n != crashLineBudget {
		t.Errorf("logged %d crash lines, want %d", n, crashLineBudget)
	}
	if strings.Contains(buf.String(), "goroutine 1") {
		t.Error("line before the crash was logged")
	}
}
