package main

import (
	"bytes"
	"testing"
)

// escapeAll runs data through a fresh escaper in chunks of the given
// size (0 = one write) and returns what reached the writer.
func escapeAll(t *testing.T, data []byte, chunk int) []byte {
	t.Helper()
	var out bytes.Buffer
	e := newNALEscaper(&out)
	if chunk <= 0 {
		chunk = len(data)
	}
	for i := 0; i < len(data); i += chunk {
		end := i + chunk
		if end > len(data) {
			end = len(data)
		}
		n, err := e.Write(data[i:end])
		if err != nil || n != end-i {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	return out.Bytes()
}

// nalUnits splits Annex B bytes at 00 00 01 and returns the NAL units
// without their start codes and trailing zero bytes.
func nalUnits(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	emit := func(end int) {
		if start < 0 {
			return
		}
		n := bytes.TrimRight(b[start:end], "\x00")
		if len(n) > 0 {
			nals = append(nals, n)
		}
	}
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			emit(i)
			start = i + 3
			i += 2
		}
	}
	emit(len(b))
	return nals
}

// assertLegal fails when a NAL unit holds a sequence H.264 7.4.1 forbids.
func assertLegal(t *testing.T, nals [][]byte) {
	t.Helper()
	for k, n := range nals {
		for i := 2; i < len(n); i++ {
			if n[i-2] == 0 && n[i-1] == 0 && n[i] < 3 {
				t.Fatalf("NAL %d (type %d): 00 00 %02x at byte %d", k, n[0]&0x1f, n[i], i)
			}
			if n[i-2] == 0 && n[i-1] == 0 && n[i] == 3 && i+1 < len(n) && n[i+1] > 3 {
				t.Fatalf("NAL %d (type %d): 00 00 03 %02x at byte %d", k, n[0]&0x1f, n[i+1], i)
			}
		}
	}
}

func TestNALEscaperInsertsPreventionBytesInZeroRuns(t *testing.T) {
	in := []byte{
		0, 0, 0, 1, 0x09, 0xf0, // AUD
		0, 0, 0, 1, 0x41, 0x9a, 0, 0, 0, 0, 0, 0x80, 0x11, // P slice with an unescaped zero run
		0, 0, 0, 1, 0x09, 0xf0, // next AUD releases the slice
	}
	got := escapeAll(t, in, 0)
	want := []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 0, 1, 0x41, 0x9a, 0, 0, 3, 0, 0, 3, 0, 0x80, 0x11,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestNALEscaperKeepsLegalNALUnits(t *testing.T) {
	in := []byte{
		0, 0, 0, 1, 0x67, 0x64, 0, 0x29, 0xac, // SPS
		0, 0, 1, 0x68, 0xee, 0x3c, 0x80, // PPS, 3-byte start code
		0, 0, 0, 1, 0x65, 0x88, 0x80, 0, 0, 3, 1, 0x59, // IDR with a valid escape
		0, 0, 0, 1, 0x09, 0xf0,
	}
	got := escapeAll(t, in, 0)
	want := []byte{
		0, 0, 0, 1, 0x67, 0x64, 0, 0x29, 0xac,
		0, 0, 0, 1, 0x68, 0xee, 0x3c, 0x80,
		0, 0, 0, 1, 0x65, 0x88, 0x80, 0, 0, 3, 1, 0x59,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestNALEscaperDropsInvalidNALUnits(t *testing.T) {
	in := []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 1, 0xc0, 0x55, // forbidden_zero_bit set (IoTVideo residue)
		0, 0, 1, 0x00, 0x55, // nal_unit_type 0
		0, 0, 0, 1, 0x41, 0x9a,
		0, 0, 0, 1, 0x09, 0xf0,
	}
	got := nalUnits(escapeAll(t, in, 0))
	if len(got) != 2 || got[0][0] != 0x09 || got[1][0] != 0x41 {
		t.Fatalf("got NAL units % x", got)
	}
}

func TestNALEscaperPassesBytesWithoutStartCode(t *testing.T) {
	in := []byte{0x12, 0x34, 0x56, 0x78, 0x9a}
	var out bytes.Buffer
	e := newNALEscaper(&out)
	_, _ = e.Write(in)
	_, _ = e.Write([]byte{0, 0, 0, 1, 0x09, 0xf0, 0, 0, 1, 0x41, 0x9a, 0, 0, 1})
	if !bytes.Equal(out.Bytes(), append(append([]byte{}, in...), 0, 0, 0, 1, 0x09, 0xf0, 0, 0, 0, 1, 0x41, 0x9a)) {
		t.Fatalf("got % x", out.Bytes())
	}
}

func TestNALEscaperDropsFramesWithoutSlice(t *testing.T) {
	in := []byte{
		0, 0, 0, 1, 0x09, 0xf0, // AUD of a frame whose slices were lost
		0, 0, 0, 1, 0x06, 0x05, 0x80, // its SEI
		0, 0, 0, 1, 0x67, 0x64, 0x80, // SPS
		0, 0, 0, 1, 0x68, 0xee, 0x80, // PPS
		0, 0, 0, 1, 0x09, 0xf0, // next AUD
		0, 0, 0, 1, 0x65, 0x88, 0x80, // IDR slice
		0, 0, 0, 1, 0x09, 0xf0,
	}
	got := nalUnits(escapeAll(t, in, 0))
	var types []byte
	for _, n := range got {
		types = append(types, n[0]&0x1f)
	}
	if !bytes.Equal(types, []byte{7, 8, 9, 5}) {
		t.Fatalf("NAL types %v, want [7 8 9 5]", types)
	}
}

func TestNALEscaperDropsTypesACameraNeverSends(t *testing.T) {
	in := []byte{
		0, 0, 0, 1, 0x09, 0xf0,
		0, 0, 1, 0x1d, 0x41, 0x42, // type 29 (FU-B), framing residue
		0, 0, 1, 0x0e, 0x41, // type 14 (SVC prefix)
		0, 0, 0, 1, 0x41, 0x9a,
		0, 0, 0, 1, 0x09, 0xf0,
	}
	got := nalUnits(escapeAll(t, in, 0))
	if len(got) != 2 || got[0][0] != 0x09 || got[1][0] != 0x41 {
		t.Fatalf("got NAL units % x", got)
	}
}
