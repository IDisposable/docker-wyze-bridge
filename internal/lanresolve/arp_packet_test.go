package lanresolve

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestARPRequest_Layout(t *testing.T) {
	// Requests go out laid out byte for byte per RFC 826: fixed header,
	// our MAC and IP as sender, a zeroed target MAC, then the target IP.
	hw := net.HardwareAddr{0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	pkt := arpRequest(hw, net.ParseIP("192.168.4.10"), net.ParseIP("192.168.4.151"))
	if len(pkt) != arpPacketLen {
		t.Fatalf("len = %d, want %d", len(pkt), arpPacketLen)
	}
	// Fixed header per RFC 826: Ethernet, IPv4, 6, 4, request.
	want := []byte{0x00, 0x01, 0x08, 0x00, 0x06, 0x04, 0x00, 0x01}
	if !bytes.Equal(pkt[:8], want) {
		t.Errorf("header = % x, want % x", pkt[:8], want)
	}
	if !bytes.Equal(pkt[8:14], hw) {
		t.Errorf("sender hw = % x", pkt[8:14])
	}
	if !bytes.Equal(pkt[14:18], []byte{192, 168, 4, 10}) {
		t.Errorf("sender ip = % x", pkt[14:18])
	}
	if !bytes.Equal(pkt[18:24], make([]byte, 6)) {
		t.Errorf("target hw must be zero in a request, got % x", pkt[18:24])
	}
	if !bytes.Equal(pkt[24:28], []byte{192, 168, 4, 151}) {
		t.Errorf("target ip = % x", pkt[24:28])
	}
}

// buildReply makes a well-formed reply so the parser can be fed every
// kind of corruption from a known-good base.
func buildReply(senderHW net.HardwareAddr, senderIP net.IP) []byte {
	b := arpRequest(senderHW, senderIP, net.IPv4(1, 1, 1, 1))
	binary.BigEndian.PutUint16(b[6:], arpOpReply)
	return b
}

func TestParseARPReply(t *testing.T) {
	// A good reply parses, even with Ethernet padding, into copies the
	// next read into the buffer can't overwrite. Short frames, requests
	// and wrong header fields are all rejected.
	hw := net.HardwareAddr{0xd0, 0x3f, 0x27, 0x00, 0xca, 0x04}
	good := buildReply(hw, net.ParseIP("192.168.4.151"))

	r, ok := parseARPReply(good)
	if !ok {
		t.Fatal("well-formed reply rejected")
	}
	if macString(r.SenderHW) != "d03f2700ca04" || r.SenderIP.String() != "192.168.4.151" {
		t.Errorf("parsed %s / %s", macString(r.SenderHW), r.SenderIP)
	}

	// Trailing padding (Ethernet minimum frame size) is fine.
	if _, ok := parseARPReply(append(good, make([]byte, 18)...)); !ok {
		t.Error("padded reply rejected")
	}

	// The parser must hand back its own copies, not aliases of the
	// receive buffer that the next read will overwrite.
	good[8], good[14] = 0xff, 0xff
	if macString(r.SenderHW) != "d03f2700ca04" || r.SenderIP.String() != "192.168.4.151" {
		t.Error("parsed fields alias the input buffer")
	}

	bad := map[string]func([]byte){
		"truncated":         func(b []byte) { b[0] = 0 },
		"request not reply": func(b []byte) { binary.BigEndian.PutUint16(b[6:], arpOpRequest) },
		"ipv6 ptype":        func(b []byte) { binary.BigEndian.PutUint16(b[2:], 0x86dd) },
		"token ring htype":  func(b []byte) { binary.BigEndian.PutUint16(b[0:], 6) },
		"hlen 8":            func(b []byte) { b[4] = 8 },
		"plen 16":           func(b []byte) { b[5] = 16 },
	}
	for name, corrupt := range bad {
		b := buildReply(hw, net.ParseIP("192.168.4.151"))
		if name == "truncated" {
			b = b[:arpPacketLen-1]
		} else {
			corrupt(b)
		}
		if _, ok := parseARPReply(b); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, ok := parseARPReply(nil); ok {
		t.Error("nil accepted")
	}
}
