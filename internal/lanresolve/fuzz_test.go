package lanresolve

import (
	"encoding/binary"
	"strings"
	"testing"
)

// The resolver's two wire parsers eat whatever the LAN and the kernel
// send; neither may panic or hand back a half-formed answer. These run
// with the seed corpus under plain `go test` and dig further under
// `go test -fuzz=FuzzParseARPReply ./internal/lanresolve/`.

func FuzzParseARPReply(f *testing.F) {
	reply := make([]byte, arpPacketLen)
	binary.BigEndian.PutUint16(reply[0:], arpHTypeEthernet)
	binary.BigEndian.PutUint16(reply[2:], arpPTypeIPv4)
	reply[4], reply[5] = 6, 4
	binary.BigEndian.PutUint16(reply[6:], arpOpReply)
	copy(reply[8:14], []byte{0xd0, 0x3f, 0x27, 0x00, 0xca, 0x04})
	copy(reply[14:18], []byte{192, 168, 4, 151})
	f.Add(reply)
	f.Add(reply[:10])
	f.Add([]byte{})
	f.Add(append(append([]byte{}, reply...), 0xff, 0xff))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, ok := parseARPReply(b)
		if !ok {
			return
		}
		if len(r.SenderHW) != 6 || len(r.SenderIP) != 4 {
			t.Fatalf("malformed reply accepted: %+v", r)
		}
		if len(b) < arpPacketLen {
			t.Fatalf("short frame (%d bytes) accepted", len(b))
		}
	})
}

func FuzzParseNeighbor(f *testing.F) {
	// A well-formed RTM_NEWNEIGH payload: ndmsg header then one NDA_DST
	// and one NDA_LLADDR attribute.
	hdr := make([]byte, ndmsgLen)
	hdr[0] = 2 // AF_INET
	binary.NativeEndian.PutUint32(hdr[4:8], 3)
	binary.NativeEndian.PutUint16(hdr[8:10], 0x02) // REACHABLE
	dst := make([]byte, 8)
	binary.NativeEndian.PutUint16(dst[0:2], 8)
	binary.NativeEndian.PutUint16(dst[2:4], ndaDst)
	copy(dst[4:], []byte{192, 168, 4, 151})
	ll := make([]byte, 12)
	binary.NativeEndian.PutUint16(ll[0:2], 10)
	binary.NativeEndian.PutUint16(ll[2:4], ndaLLAddr)
	copy(ll[4:10], []byte{0xd0, 0x3f, 0x27, 0x00, 0xca, 0x04})
	good := append(append(append([]byte{}, hdr...), dst...), ll...)
	f.Add(good)
	f.Add(good[:len(good)-3])
	f.Add(hdr)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		n, ok := parseNeighbor(b)
		if !ok {
			return
		}
		if len(n.IP) != 4 {
			t.Fatalf("entry without a 4-byte IP accepted: %+v", n)
		}
		if n.MAC != "" && (len(n.MAC) != 12 || strings.Trim(n.MAC, "0123456789abcdef") != "") {
			t.Fatalf("MAC not normalized: %q", n.MAC)
		}
		if len(b) < ndmsgLen {
			t.Fatalf("short payload (%d bytes) accepted", len(b))
		}
	})
}

func FuzzNormalizeMAC(f *testing.F) {
	// Whatever string comes in, the result is "" or 12 lowercase hex
	// digits, and normalizing it a second time changes nothing.
	for _, s := range []string{"D03F2700CA04", "d0:3f:27:00:ca:04", "GW_DBD_80482C0D6B01", "", "zz", "  D0-3F-27-00-CA-04  ", "ffffffffffffff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := NormalizeMAC(s)
		if got != "" && (len(got) != 12 || strings.Trim(got, "0123456789abcdef") != "") {
			t.Fatalf("NormalizeMAC(%q) = %q", s, got)
		}
		if again := NormalizeMAC(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, got, again)
		}
	})
}
