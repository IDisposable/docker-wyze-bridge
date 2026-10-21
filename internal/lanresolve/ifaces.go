package lanresolve

import (
	"net"
)

// lanInterface is one interface the resolver can reach cameras through:
// up, carries a hardware address (so it can speak ARP), and has an IPv4
// subnet. That rules out loopback, tunnels and WireGuard, which cameras
// never sit behind anyway.
type lanInterface struct {
	Index int
	Name  string
	HW    net.HardwareAddr
	IP    net.IP     // our IPv4 address on this interface, 4-byte form
	Net   *net.IPNet // the subnet that address belongs to
}

// sweepMaxPrefix is the smallest prefix (largest subnet) the sweep will
// walk in full: /22 is 1022 hosts, which is eero's default LAN size and
// about two seconds of paced ARP. Anything wider (a /16 on docker0,
// say) only gets the /24 around each hint, never the whole range.
const sweepMaxPrefix = 22

// listInterfaces enumerates the host's LAN-facing interfaces. One entry
// per IPv4 address, so an interface with two addresses appears twice
// with the same Index — ARP on either works, and the sweep dedupes by
// subnet anyway.
func listInterfaces() ([]lanInterface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []lanInterface
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifi.HardwareAddr) != 6 {
			continue // no Ethernet-style address: tun, wg, ppp
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLinkLocalUnicast() {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if bits != 32 || ones == 32 {
				continue // not IPv4, or a host route with nothing to sweep
			}
			out = append(out, lanInterface{
				Index: ifi.Index,
				Name:  ifi.Name,
				HW:    ifi.HardwareAddr,
				IP:    ip4,
				Net:   &net.IPNet{IP: ip4.Mask(ipn.Mask), Mask: ipn.Mask},
			})
		}
	}
	return out, nil
}

// onLink returns the interfaces whose subnet contains ip. Empty means
// the address is routed (another household's 192.168.0.x, for example)
// and ARP can't reach it from here at all.
func onLink(ifaces []lanInterface, ip net.IP) []lanInterface {
	var out []lanInterface
	for _, ifi := range ifaces {
		if ifi.Net.Contains(ip) {
			out = append(out, ifi)
		}
	}
	return out
}

// slash24 returns the /24 that contains ip. DHCP servers hand out
// addresses from a pool, and a camera that moved almost always moved
// within the same /24, so that block is swept before anything else.
func slash24(ip net.IP) *net.IPNet {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	mask := net.CIDRMask(24, 32)
	return &net.IPNet{IP: ip4.Mask(mask), Mask: mask}
}

// hosts lists the usable addresses of a subnet in ascending order,
// skipping the network and broadcast addresses, and stops at max.
func hosts(n *net.IPNet, max int) []net.IP {
	ones, bits := n.Mask.Size()
	if bits != 32 || max <= 0 {
		return nil
	}
	base := ipToUint32(n.IP)
	size := uint32(1) << uint(32-ones)
	first, last := base, base+size-1
	if ones <= 30 {
		first, last = base+1, base+size-2 // drop network + broadcast
	}
	var out []net.IP
	for a := first; a <= last && len(out) < max; a++ {
		out = append(out, uint32ToIP(a))
		if a == ^uint32(0) {
			break // guard against wraparound on 255.255.255.255
		}
	}
	return out
}

// sweepAddresses builds the ordered, deduplicated address list a sweep
// walks: the /24 around each hint first (clipped to the local subnet it
// falls in), then the remainder of every local subnet no wider than
// sweepMaxPrefix, capped at max addresses in total. Hints that aren't
// on any local subnet contribute nothing — ARP can't reach them.
func sweepAddresses(ifaces []lanInterface, hints []net.IP, max int) []net.IP {
	seen := make(map[uint32]bool)
	var out []net.IP
	add := func(ips []net.IP, within *net.IPNet) {
		for _, ip := range ips {
			if len(out) >= max {
				return
			}
			if within != nil && !within.Contains(ip) {
				continue
			}
			k := ipToUint32(ip)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, ip)
		}
	}
	for _, h := range hints {
		h4 := h.To4()
		if h4 == nil {
			continue
		}
		for _, ifi := range onLink(ifaces, h4) {
			if ifi.IP.Equal(h4) {
				continue // the hint is us, not a camera
			}
			add(hosts(slash24(h4), max), ifi.Net)
		}
	}
	for _, ifi := range ifaces {
		if ones, _ := ifi.Net.Mask.Size(); ones < sweepMaxPrefix {
			continue
		}
		add(hosts(ifi.Net, max), nil)
	}
	// Never ARP for our own addresses; a reply from ourselves would only
	// confuse the MAC match.
	filtered := out[:0]
	for _, ip := range out {
		self := false
		for _, ifi := range ifaces {
			if ifi.IP.Equal(ip) {
				self = true
				break
			}
		}
		if !self {
			filtered = append(filtered, ip)
		}
	}
	return filtered
}

func ipToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}

func uint32ToIP(a uint32) net.IP {
	return net.IPv4(byte(a>>24), byte(a>>16), byte(a>>8), byte(a)).To4()
}

// LocalSubnets returns the IPv4 subnets of the host's LAN-facing
// interfaces (see listInterfaces), deduplicated. The camera manager's
// "local subnets only" rule uses it to decide which cameras on a Wyze
// account are reachable from here at all.
func LocalSubnets() ([]*net.IPNet, error) {
	ifaces, err := listInterfaces()
	if err != nil {
		return nil, err
	}
	var out []*net.IPNet
	seen := make(map[string]bool)
	for _, ifi := range ifaces {
		key := ifi.Net.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ifi.Net)
	}
	return out, nil
}
