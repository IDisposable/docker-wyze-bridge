package webui

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
)

// The dashboard's players talk to go2rtc through the /ws proxy, and
// when go2rtc can't reach a camera it tells the player so in a JSON
// text frame ({"type":"error","value":"mse: streams: wyze: connect
// failed: discovery timeout"}) and nothing else: the consumer never
// shows up in /api/streams and the log line, if any, doesn't name the
// stream. So the proxy reads those frames off the bytes it is already
// copying to the browser. wsSniffer is an io.Writer fed through
// io.TeeReader on the go2rtc → browser direction; it skips the HTTP
// 101 response, then walks WebSocket frames and hands every text
// frame to onText. It never alters the bytes and never blocks the copy.

// wsSniffTextMax bounds the text frame the sniffer will buffer; an
// error message is a few hundred bytes, media frames are binary and
// skipped by length.
const wsSniffTextMax = 16 << 10

type wsSniffer struct {
	onText func(text string)
	buf    []byte
	// upgraded is true once the "\r\n\r\n" ending the 101 response has
	// gone by; before that the bytes are HTTP, not frames.
	upgraded bool
	// skip is how many payload bytes of a frame we're not interested in
	// remain to be discarded.
	skip int
}

func newWSSniffer(onText func(text string)) *wsSniffer {
	return &wsSniffer{onText: onText}
}

// Write consumes a chunk of the upstream stream; chunk boundaries are
// arbitrary, so state carries over between calls. It always reports
// the whole chunk as written so the TeeReader copy never stalls.
func (s *wsSniffer) Write(p []byte) (int, error) {
	s.feed(p)
	return len(p), nil
}

func (s *wsSniffer) feed(p []byte) {
	if !s.upgraded {
		s.buf = append(s.buf, p...)
		i := bytes.Index(s.buf, []byte("\r\n\r\n"))
		if i < 0 {
			if len(s.buf) > wsSniffTextMax {
				// Not an upgrade response after all; stop looking.
				s.buf = nil
				s.skip = -1
			}
			return
		}
		s.upgraded = true
		p = append([]byte(nil), s.buf[i+4:]...)
		s.buf = nil
	}
	if s.skip < 0 {
		return // gave up
	}
	s.buf = append(s.buf, p...)
	for {
		if s.skip > 0 {
			n := min(s.skip, len(s.buf))
			s.buf = s.buf[n:]
			s.skip -= n
			if s.skip > 0 {
				return
			}
		}
		hdr, payload, ok := wsFrameHeader(s.buf)
		if !ok {
			return // need more bytes for the header
		}
		opcode := s.buf[0] & 0x0f
		masked := s.buf[1]&0x80 != 0
		if opcode != 1 || payload > wsSniffTextMax || masked {
			// Binary (media), control, oversized or client-style frame:
			// drop its payload without buffering it.
			s.buf = s.buf[hdr:]
			s.skip = int(payload)
			continue
		}
		if len(s.buf) < hdr+int(payload) {
			return // text frame still arriving
		}
		text := string(s.buf[hdr : hdr+int(payload)])
		s.buf = s.buf[hdr+int(payload):]
		if s.onText != nil {
			s.onText(text)
		}
	}
}

// wsFrameHeader parses a frame header from the start of b. Returns the
// header length and payload length, or ok=false when b doesn't yet
// hold the whole header.
func wsFrameHeader(b []byte) (hdr int, payload uint64, ok bool) {
	if len(b) < 2 {
		return 0, 0, false
	}
	hdr = 2
	payload = uint64(b[1] & 0x7f)
	switch payload {
	case 126:
		if len(b) < 4 {
			return 0, 0, false
		}
		payload = uint64(binary.BigEndian.Uint16(b[2:4]))
		hdr = 4
	case 127:
		if len(b) < 10 {
			return 0, 0, false
		}
		payload = binary.BigEndian.Uint64(b[2:10])
		hdr = 10
	}
	if b[1]&0x80 != 0 {
		hdr += 4 // masking key (clients only; servers never mask)
	}
	if len(b) < hdr {
		return 0, 0, false
	}
	return hdr, payload, true
}

// go2rtcWSError extracts the camera-facing text of a go2rtc WebSocket
// error frame, or "" when the frame isn't one. go2rtc prefixes the
// error with the request type ("mse: ", "webrtc: ") and the streams
// package adds "streams: "; both go, the same way ParseStreamError
// trims the log form.
func go2rtcWSError(text string) string {
	var msg struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(text), &msg); err != nil || msg.Type != "error" {
		return ""
	}
	v := strings.TrimSpace(msg.Value)
	for _, prefix := range []string{"mse: ", "webrtc: ", "mjpeg: ", "hls: ", "mp4: ", "streams: "} {
		v = strings.TrimPrefix(v, prefix)
	}
	v = strings.TrimPrefix(v, "streams: ")
	return v
}
