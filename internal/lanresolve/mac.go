package lanresolve

import (
	"encoding/hex"
	"net"
	"strings"
)

// NormalizeMAC reduces the many spellings of a hardware address to the
// one the resolver compares on: twelve lowercase hex digits, no
// separators. The Wyze cloud reports "D03F2700CA04", the kernel's
// neighbor cache reports "d0:3f:27:00:ca:04", and Gwell-lineage device
// IDs carry a model prefix like "GW_DBD_80482C0D6B01" — the trailing
// twelve hex digits are the address in every case. Returns "" when the
// input doesn't contain at least twelve hex digits, so callers can use
// the empty string as "no usable MAC" without a second check.
func NormalizeMAC(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	h := b.String()
	if len(h) < 12 {
		return ""
	}
	return h[len(h)-12:]
}

// macString renders raw hardware bytes in the normalized form so ARP
// replies and neighbor entries compare equal to NormalizeMAC output.
func macString(hw net.HardwareAddr) string {
	if len(hw) != 6 {
		return ""
	}
	return hex.EncodeToString(hw)
}

// isZeroMAC reports whether every byte is zero — what the neighbor cache
// shows for an entry the kernel never resolved.
func isZeroMAC(hw net.HardwareAddr) bool {
	for _, b := range hw {
		if b != 0 {
			return false
		}
	}
	return true
}
