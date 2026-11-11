package main

import (
	"io"
)

// nalEscaper sits between the P2P session and the ffmpeg publisher and
// makes every H.264 NAL unit legal Annex B before it leaves the proxy.
//
// Wyze Gwell cameras (Wyze Cam OG, GW_GC1) send slice NAL units with
// runs of zero bytes that carry no emulation prevention bytes: "00 00 00"
// inside a NAL unit, which H.264 7.4.1 forbids. ffmpeg's software decoder
// accepts it, so the stream looks fine in VLC. Hardware decoders do not:
// Apple VideoToolbox (Chrome and Safari on macOS and iOS) rejects every
// such frame with kVTVideoDecoderBadDataErr, and HLS in the browser never
// shows a picture.
//
// The escaper splits the byte stream at start codes, and for each NAL
// unit it:
//   - trims trailing zero bytes (they belong to the next start code),
//   - drops units that cannot be H.264 (forbidden_zero_bit set, or
//     nal_unit_type 0: residue of the IoTVideo framing) and the types a
//     camera never sends (13..31: SVC/MVC, reserved, and the RTP
//     aggregation types such as FU-B that the framing residue mimics),
//   - drops an access unit delimiter (and its SEI) that no slice follows
//     before the next delimiter: a frame whose slices the relay lost.
//     Such a frame has no picture, and mediamtx closes its HLS muxer on
//     it ("access unit doesn't contain an IDR or non-IDR NALU"). SPS and
//     PPS are kept and go out with the next slice,
//   - removes and re-inserts emulation prevention bytes (0x03), the same
//     rewrite ffmpeg's CBS bitstream filters (h264_metadata) do,
//
// and writes it with a 4-byte start code. It does not reorder or pace
// anything: a slice goes out as soon as the next start code arrives, the
// NAL units before it (AUD, SPS, PPS, SEI) with it. Bytes with no start
// code pass through unchanged.
//
// Make one escaper per P2P session: a partial NAL unit left by a dead
// session must not be glued to the first bytes of the next one.
type nalEscaper struct {
	out io.Writer

	pending []byte // bytes not yet written, from the current NAL start
	start   int    // index in pending of the first byte of the current NAL; -1 before the first start code
	scan    int    // index in pending where the start-code search continues

	held [][]byte // non-VCL NAL units (escaped, with start code) waiting for the next slice
}

// nalEscaperMaxHeld bounds the held units when no slice arrives.
const nalEscaperMaxHeld = 32

// nalEscaperMaxPending bounds the buffer when no start code arrives (not
// Annex B, or a corrupt stream). The buffer is then flushed unchanged.
const nalEscaperMaxPending = 4 << 20

func newNALEscaper(out io.Writer) *nalEscaper {
	return &nalEscaper{out: out, start: -1}
}

// Write accepts Annex B bytes in any chunking. It reports len(b) as
// written when the downstream write succeeds.
func (e *nalEscaper) Write(b []byte) (int, error) {
	e.pending = append(e.pending, b...)
	var out []byte
	for {
		sc, scLen := findStartCode(e.pending, e.scan)
		if sc < 0 {
			break
		}
		if e.start < 0 {
			// Bytes before the first start code are not a NAL unit:
			// pass them through as they are.
			out = append(out, e.pending[:sc]...)
		} else {
			out = e.appendNAL(out, e.pending[e.start:sc])
		}
		e.start = sc + scLen
		e.scan = e.start
	}
	// Keep only the open NAL unit (or the bytes before the first start
	// code). Keep 2 extra bytes: a start code can span two writes.
	keepFrom := e.start
	if keepFrom < 0 {
		keepFrom = 0
	}
	if e.start >= 0 {
		e.scan = len(e.pending) - 2
		if e.scan < e.start {
			e.scan = e.start
		}
	} else {
		e.scan = len(e.pending) - 2
		if e.scan < 0 {
			e.scan = 0
		}
	}
	if len(e.pending)-keepFrom > nalEscaperMaxPending {
		// No start code for too long: give up on this chunk.
		out = append(out, e.pending[keepFrom:]...)
		keepFrom = len(e.pending)
		e.start = -1
		e.scan = 0
	}
	if keepFrom > 0 {
		rest := copy(e.pending, e.pending[keepFrom:])
		e.pending = e.pending[:rest]
		e.scan -= keepFrom
		if e.start >= 0 {
			e.start -= keepFrom
		}
		if e.scan < 0 {
			e.scan = 0
		}
	}
	if len(out) > 0 {
		if _, err := e.out.Write(out); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// findStartCode returns the index of the next Annex B start code at or
// after from, and its length (3 for 00 00 01, 4 for 00 00 00 01), or -1.
func findStartCode(b []byte, from int) (int, int) {
	for i := from; i+2 < len(b); i++ {
		if b[i+2] > 1 {
			i += 2 // b[i+2] cannot be part of a start code prefix
			continue
		}
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if i > from && b[i-1] == 0 {
				return i - 1, 4
			}
			return i, 3
		}
	}
	return -1, 0
}

// appendNAL appends one NAL unit (without its start code) to out with a
// 4-byte start code, holds it until the next slice, or drops it.
func (e *nalEscaper) appendNAL(out, nal []byte) []byte {
	for len(nal) > 0 && nal[len(nal)-1] == 0 {
		nal = nal[:len(nal)-1]
	}
	if len(nal) == 0 || nal[0]&0x80 != 0 {
		return out
	}
	switch t := nal[0] & 0x1f; {
	case t >= 1 && t <= 5: // slice: the held units of its access unit go first
		for _, h := range e.held {
			out = append(out, h...)
		}
		e.held = e.held[:0]
		out = append(out, 0, 0, 0, 1)
		return appendEscaped(out, nal)
	case t == 9: // AUD: an earlier AUD that got no slice is a lost frame
		kept := e.held[:0]
		for _, h := range e.held {
			if ht := h[4] & 0x1f; ht == 7 || ht == 8 {
				kept = append(kept, h)
			}
		}
		e.held = kept
	case t >= 6 && t <= 12: // SEI, SPS, PPS, end of sequence/stream, filler
	default: // 0 and 13..31: not from a camera encoder
		return out
	}
	if len(e.held) >= nalEscaperMaxHeld {
		e.held = append(e.held[:0], e.held[1:]...)
	}
	e.held = append(e.held, appendEscaped([]byte{0, 0, 0, 1}, nal))
	return out
}

// appendEscaped appends nal to out with valid emulation prevention.
//
// It first reads nal as escaped data (an 0x03 after two zero bytes is an
// emulation prevention byte and is dropped), then escapes the result:
// an 0x03 goes before any byte 0x00..0x03 that follows two zero bytes.
// The NAL header and a payload that is already legal come out unchanged.
func appendEscaped(out, nal []byte) []byte {
	if !needsEscape(nal) {
		return append(out, nal...)
	}
	zerosIn := 0  // zero run in the input (for unescaping)
	zerosOut := 0 // zero run in the output (for escaping)
	for _, c := range nal {
		if zerosIn >= 2 && c == 3 {
			zerosIn = 0
			continue // emulation prevention byte from the camera
		}
		if c == 0 {
			zerosIn++
		} else {
			zerosIn = 0
		}
		if zerosOut >= 2 && c <= 3 {
			out = append(out, 3)
			zerosOut = 0
		}
		out = append(out, c)
		if c == 0 {
			zerosOut++
		} else {
			zerosOut = 0
		}
	}
	return out
}

// needsEscape reports whether nal contains a zero run that is not
// followed by a valid emulation prevention byte, or an 0x03 that is not
// one. Legal NAL units (the common case) skip the rewrite.
func needsEscape(nal []byte) bool {
	for i := 2; i < len(nal); i++ {
		if nal[i-2] != 0 || nal[i-1] != 0 {
			continue
		}
		switch c := nal[i]; {
		case c < 3:
			return true
		case c == 3:
			// 00 00 03 must be followed by 00..03 or end the unit.
			if i+1 < len(nal) && nal[i+1] > 3 {
				return true
			}
			i++ // skip the escape byte, zero run restarts after it
		}
	}
	return false
}
