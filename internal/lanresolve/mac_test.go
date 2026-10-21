package lanresolve

import (
	"net"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	// Every MAC spelling we see (cloud, kernel, Windows, Cisco, Gwell ids
	// with a prefix) comes out as the same 12 hex digits. Longer input
	// keeps its tail, and junk or too-short input comes back empty.
	cases := []struct {
		in, want string
	}{
		{"D03F2700CA04", "d03f2700ca04"},         // Wyze cloud form
		{"d0:3f:27:00:ca:04", "d03f2700ca04"},    // kernel / ip neigh form
		{"D0-3F-27-00-CA-04", "d03f2700ca04"},    // Windows form
		{"d03f.2700.ca04", "d03f2700ca04"},       // Cisco form
		{"GW_DBD_80482C0D6B01", "80482c0d6b01"},  // Gwell device id with model prefix
		{" d0:3f:27:00:ca:04\n", "d03f2700ca04"}, // stray whitespace
		{"", ""},
		{"zz:zz", ""},
		{"d03f27a8816", ""},                  // 11 hex digits: too short
		{"0000d03f2700ca04", "d03f2700ca04"}, // longer than 12: keep the tail
	}
	for _, c := range cases {
		if got := NormalizeMAC(c.in); got != c.want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMACString_RoundTripsNormalizeMAC(t *testing.T) {
	// A MAC read off the wire comes out spelled the way NormalizeMAC
	// spells the cloud's, so the two compare directly. A non-Ethernet
	// address gives "".
	hw := net.HardwareAddr{0xd0, 0x3f, 0x27, 0x00, 0xca, 0x04}
	if got := macString(hw); got != NormalizeMAC("D0:3F:27:00:CA:04") {
		t.Errorf("macString = %q, want the normalized form", got)
	}
	if got := macString(net.HardwareAddr{1, 2, 3}); got != "" {
		t.Errorf("macString of a non-Ethernet address = %q, want \"\"", got)
	}
}

func TestIsZeroMAC(t *testing.T) {
	// Only an all-zero or empty hardware address counts as zero; a
	// single set bit makes it a real MAC.
	if !isZeroMAC(net.HardwareAddr{0, 0, 0, 0, 0, 0}) {
		t.Error("all-zero should be zero")
	}
	if isZeroMAC(net.HardwareAddr{0, 0, 0, 0, 0, 1}) {
		t.Error("non-zero should not be zero")
	}
	if !isZeroMAC(nil) {
		t.Error("nil (no bytes) counts as zero")
	}
}
