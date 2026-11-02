package webui

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

// wsFrame builds an unmasked server frame.
func wsFrame(opcode byte, payload []byte) []byte {
	b := []byte{0x80 | opcode}
	switch n := len(payload); {
	case n < 126:
		b = append(b, byte(n))
	case n < 1<<16:
		b = append(b, 126, 0, 0)
		binary.BigEndian.PutUint16(b[2:], uint16(n))
	default:
		b = append(b, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(b[2:], uint64(n))
	}
	return append(b, payload...)
}

func TestWSSniffer_FindsTextFramesAcrossChunkBoundaries(t *testing.T) {
	// The sniffer pulls every text frame out of the stream even when
	// headers and payloads are split across writes, skipping media and
	// control frames of every length encoding.
	var got []string
	s := newWSSniffer(func(text string) { got = append(got, text) })
	stream := []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	stream = append(stream, wsFrame(1, []byte(`{"type":"mse","value":"video/mp4"}`))...)
	stream = append(stream, wsFrame(2, make([]byte, 70000))...) // media, 64-bit length
	stream = append(stream, wsFrame(2, make([]byte, 300))...)   // media, 16-bit length
	stream = append(stream, wsFrame(1, []byte(`{"type":"error","value":"mse: streams: wyze: connect failed: discovery timeout"}`))...)
	stream = append(stream, wsFrame(9, []byte("ping"))...) // control
	stream = append(stream, wsFrame(1, []byte(`last`))...)

	// Feed it in awkward pieces: byte by byte for the first 60, then 7
	// at a time, so headers and payloads split everywhere.
	for i := 0; i < len(stream); {
		step := 7
		if i < 60 {
			step = 1
		}
		end := min(i+step, len(stream))
		if n, err := s.Write(stream[i:end]); err != nil || n != end-i {
			t.Fatalf("Write returned %d, %v", n, err)
		}
		i = end
	}
	if len(got) != 3 || !strings.Contains(got[0], "video/mp4") || !strings.Contains(got[1], "discovery timeout") || got[2] != "last" {
		t.Errorf("text frames = %q", got)
	}
}

func TestWSSniffer_GivesUpOnNonHTTP(t *testing.T) {
	// If the stream never shows an HTTP upgrade the sniffer stops looking
	// instead of reading frames out of junk.
	calls := 0
	s := newWSSniffer(func(string) { calls++ })
	junk := make([]byte, wsSniffTextMax+10)
	for i := range junk {
		junk[i] = 'x'
	}
	s.Write(junk)
	s.Write(wsFrame(1, []byte(`{"type":"error","value":"nope"}`)))
	if calls != 0 || s.skip != -1 {
		t.Errorf("sniffer should give up on a stream that never upgrades: calls=%d skip=%d", calls, s.skip)
	}
}

func TestGo2rtcWSError(t *testing.T) {
	// Only go2rtc error frames yield a message, with the mode and streams
	// prefixes trimmed so the camera shows the real cause.
	for in, want := range map[string]string{
		`{"type":"error","value":"mse: streams: wyze: connect failed: discovery timeout"}`: "wyze: connect failed: discovery timeout",
		`{"type":"error","value":"webrtc: streams: websocket: close 1006"}`:                "websocket: close 1006",
		`{"type":"error","value":"  plain  "}`:                                             "plain",
		`{"type":"mse","value":"video/mp4"}`:                                               "",
		`not json`:                                                                         "",
		`{"type":"error"}`:                                                                 "",
	} {
		if got := go2rtcWSError(in); got != want {
			t.Errorf("go2rtcWSError(%s) = %q, want %q", in, got, want)
		}
	}
}

// fakeGo2rtcWS accepts one raw upgrade, answers 101, sends the frames
// it was given, then waits until the test closes it.
func fakeGo2rtcWS(t *testing.T, frames ...[]byte) (url string, gotRequest chan string, release chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	gotRequest = make(chan string, 1)
	release = make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotRequest <- req.URL.String()
		conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
		for _, f := range frames {
			conn.Write(f)
			time.Sleep(5 * time.Millisecond)
		}
		<-release
	}()
	return "http://" + ln.Addr().String(), gotRequest, release
}

func TestHandleWSProxy_CountsPullersAndAttributesErrors(t *testing.T) {
	// A browser on the proxied socket counts as a puller until it closes,
	// go2rtc's error frame lands on the camera as its last error, and the
	// bytes still reach the browser untouched.
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "garage")
	upstreamURL, gotRequest, release := fakeGo2rtcWS(t,
		wsFrame(2, make([]byte, 500)),
		wsFrame(1, []byte(`{"type":"error","value":"mse: streams: wyze: connect failed: discovery timeout"}`)),
	)
	srv.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(upstreamURL, zerolog.Nop()))

	front := httptest.NewServer(http.HandlerFunc(srv.handleWSProxy))
	t.Cleanup(front.Close)
	client, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.Write([]byte("GET /ws?src=garage HTTP/1.1\r\nHost: bridge\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: x\r\nSec-WebSocket-Version: 13\r\n\r\n"))

	select {
	case u := <-gotRequest:
		if u != "/api/ws?src=garage" {
			t.Errorf("upstream request = %s", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never saw the upgrade")
	}
	// The browser is counted as a puller while the socket is up, and
	// go2rtc's error frame lands on the camera.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (cam.Pullers() != 1 || cam.Snapshot().LastError == "") {
		time.Sleep(10 * time.Millisecond)
	}
	if cam.Pullers() != 1 {
		t.Errorf("pullers = %d, want 1 while the socket is open", cam.Pullers())
	}
	if snap := cam.Snapshot(); snap.LastError != "wyze: connect failed: discovery timeout" || snap.Video != camera.VideoNoVideo {
		t.Errorf("error not attributed: %q video=%v", snap.LastError, snap.Video)
	}
	// Bytes still reach the browser untouched (101 + frames).
	client.SetReadDeadline(time.Now().Add(time.Second))
	head := make([]byte, 20)
	if n, _ := client.Read(head); !strings.HasPrefix(string(head[:n]), "HTTP/1.1 101") {
		t.Errorf("browser didn't get the 101: %q", head[:n])
	}

	close(release)
	client.Close()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && cam.Pullers() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if cam.Pullers() != 0 {
		t.Errorf("pullers = %d after close, want 0", cam.Pullers())
	}
}

func FuzzWSSniffer(f *testing.F) {
	// Whatever bytes come through, the sniffer's buffer stays bounded so
	// a bad stream can't grow memory without limit.
	f.Add([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n\x81\x05hello"))
	f.Add(wsFrame(1, []byte(`{"type":"error","value":"x"}`)))
	f.Add([]byte("\r\n\r\n\x82\x7f\xff\xff\xff\xff\xff\xff\xff\xff"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		s := newWSSniffer(func(string) {})
		for i := 0; i < len(b); i += 3 {
			s.Write(b[i:min(i+3, len(b))])
		}
		if len(s.buf) > 2*wsSniffTextMax+16 {
			t.Fatalf("sniffer buffer grew to %d", len(s.buf))
		}
	})
}

func TestDropStreamSockets_ClosesOnlyThatCamerasPlayers(t *testing.T) {
	// After the bridge replaces a silent stream, players on the old
	// one are cut loose so they reconnect to the new one. Other
	// cameras' players are untouched.
	srv, _ := testServer(t)
	cam := injectStreamingCamera(srv, "pet_cam")
	upstreamURL, gotRequest, release := fakeGo2rtcWS(t, wsFrame(2, make([]byte, 10)))
	t.Cleanup(func() { close(release) })
	srv.SetGo2RTCAPI(go2rtcmgr.NewAPIClient(upstreamURL, zerolog.Nop()))

	front := httptest.NewServer(http.HandlerFunc(srv.handleWSProxy))
	t.Cleanup(front.Close)
	client, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.Write([]byte("GET /ws?src=pet_cam HTTP/1.1\r\nHost: bridge\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: x\r\nSec-WebSocket-Version: 13\r\n\r\n"))
	select {
	case <-gotRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never saw the upgrade")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && cam.Pullers() != 1 {
		time.Sleep(10 * time.Millisecond)
	}

	if n := srv.DropStreamSockets("side_yard"); n != 0 {
		t.Errorf("dropped %d sockets for a camera nobody is watching", n)
	}
	if n := srv.DropStreamSockets("pet_cam"); n != 1 {
		t.Fatalf("dropped %d sockets, want 1", n)
	}
	// The browser side closes: reads drain to EOF.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, client); err != nil {
		t.Errorf("browser socket not closed: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && cam.Pullers() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if cam.Pullers() != 0 {
		t.Errorf("pullers = %d after drop", cam.Pullers())
	}
}
