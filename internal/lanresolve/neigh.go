package lanresolve

import (
	"encoding/binary"
	"net"
	"sort"
	"time"
)

// neighbor is one IPv4 entry from the kernel's neighbor cache (what
// `ip neigh` prints). Besides the address pair it carries the NUD state
// and how long ago the kernel last updated the entry, which is what lets
// the resolver prefer a fresh entry over a stale duplicate when a camera
// has moved and both its old and new addresses are still cached.
type neighbor struct {
	IP      net.IP
	MAC     string // normalized; "" for INCOMPLETE/FAILED entries
	State   uint16 // NUD_* bits
	Age     time.Duration
	Ifindex int
}

// NUD states from <linux/neighbour.h>.
const (
	nudIncomplete uint16 = 0x01
	nudReachable  uint16 = 0x02
	nudStale      uint16 = 0x04
	nudDelay      uint16 = 0x08
	nudProbe      uint16 = 0x10
	nudFailed     uint16 = 0x20
	nudNoARP      uint16 = 0x40
	nudPermanent  uint16 = 0x80

	ntfProxy = 0x08 // NTF_PROXY: a proxy-ARP entry, not a real neighbor
)

// live reports whether the kernel currently believes this mapping: it
// has a hardware address and was confirmed recently (REACHABLE), is
// being re-confirmed (DELAY/PROBE), or never expires (PERMANENT/NOARP).
func (n neighbor) live() bool {
	return n.MAC != "" && n.State&(nudReachable|nudDelay|nudProbe|nudPermanent|nudNoARP) != 0
}

// known reports whether the entry has a hardware address at all —
// live, or STALE (once confirmed, not re-verified since). STALE is a
// hint, not proof.
func (n neighbor) known() bool {
	return n.MAC != "" && (n.live() || n.State&nudStale != 0)
}

// Netlink wire layout for RTM_NEWNEIGH, from <linux/neighbour.h>:
//
//	struct ndmsg {
//	    __u8  ndm_family;   // AF_INET
//	    __u8  ndm_pad1;
//	    __u16 ndm_pad2;
//	    __s32 ndm_ifindex;
//	    __u16 ndm_state;    // NUD_*
//	    __u8  ndm_flags;    // NTF_*
//	    __u8  ndm_type;
//	};
//
// followed by rtattrs: NDA_DST (the IP), NDA_LLADDR (the MAC) and
// NDA_CACHEINFO (ages in clock ticks). All integers are host-endian.
const (
	ndmsgLen = 12

	ndaDst       = 1
	ndaLLAddr    = 2
	ndaCacheInfo = 3

	rtaAlign = 4
	rtaHdr   = 4

	// Cache ages are reported in USER_HZ ticks, which is 100 on every
	// Linux ABI regardless of the kernel's HZ.
	userHZ = 100
)

// parseNeighbor decodes one RTM_NEWNEIGH payload. Returns ok=false for
// anything the resolver doesn't want: non-IPv4, proxy entries, or a
// payload too short to carry the header.
func parseNeighbor(data []byte) (neighbor, bool) {
	if len(data) < ndmsgLen {
		return neighbor{}, false
	}
	family := data[0]
	ifindex := int(int32(binary.NativeEndian.Uint32(data[4:8])))
	state := binary.NativeEndian.Uint16(data[8:10])
	flags := data[10]
	if family != 2 /* AF_INET */ || flags&ntfProxy != 0 {
		return neighbor{}, false
	}
	n := neighbor{State: state, Ifindex: ifindex}
	// Walk the rtattr list. Each is a 4-byte header (len, type) and a
	// payload, padded to a 4-byte boundary.
	b := data[ndmsgLen:]
	for len(b) >= rtaHdr {
		l := int(binary.NativeEndian.Uint16(b[0:2]))
		t := binary.NativeEndian.Uint16(b[2:4])
		if l < rtaHdr || l > len(b) {
			break // malformed; keep what was decoded so far
		}
		payload := b[rtaHdr:l]
		switch t {
		case ndaDst:
			if len(payload) == 4 {
				n.IP = net.IPv4(payload[0], payload[1], payload[2], payload[3]).To4()
			}
		case ndaLLAddr:
			if len(payload) == 6 && !isZeroMAC(payload) {
				n.MAC = macString(net.HardwareAddr(payload))
			}
		case ndaCacheInfo:
			if len(payload) >= 12 {
				updated := binary.NativeEndian.Uint32(payload[8:12])
				n.Age = time.Duration(updated) * time.Second / userHZ
			}
		}
		next := (l + rtaAlign - 1) &^ (rtaAlign - 1)
		if next > len(b) {
			break
		}
		b = b[next:]
	}
	if n.IP == nil {
		return neighbor{}, false
	}
	return n, true
}

// findByIP returns the entry for ip, if any.
func findByIP(entries []neighbor, ip net.IP) (neighbor, bool) {
	for _, n := range entries {
		if n.IP.Equal(ip) {
			return n, true
		}
	}
	return neighbor{}, false
}

// findByMAC returns every known entry carrying mac, freshest first, so
// that when a camera moved and the old address is still cached STALE,
// the address the kernel heard from most recently is tried first.
func findByMAC(entries []neighbor, mac string) []neighbor {
	var out []neighbor
	for _, n := range entries {
		if n.known() && n.MAC == mac {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		// live beats stale; among equals, younger wins
		if out[i].live() != out[j].live() {
			return out[i].live()
		}
		return out[i].Age < out[j].Age
	})
	return out
}
