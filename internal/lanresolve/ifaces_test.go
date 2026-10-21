package lanresolve

import (
	"net"
	"testing"
)

// fakeIface builds a lanInterface from CIDR notation; the IP in the
// CIDR is "our" address on that interface.
func fakeIface(t *testing.T, index int, name, cidr string) lanInterface {
	t.Helper()
	ip, ipn, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("bad cidr %q: %v", cidr, err)
	}
	return lanInterface{
		Index: index,
		Name:  name,
		HW:    net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, byte(index)},
		IP:    ip.To4(),
		Net:   &net.IPNet{IP: ip.To4().Mask(ipn.Mask), Mask: ipn.Mask},
	}
}

func fakeIfaces(ifs ...lanInterface) func() ([]lanInterface, error) {
	return func() ([]lanInterface, error) { return ifs, nil }
}

func ipList(ips []net.IP) []string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out
}

func TestOnLink(t *testing.T) {
	// An address counts as on-link only inside one of our subnets, using
	// the real mask (a /22 covers more than its own /24). A routed
	// address matches no interface.
	ifaces := []lanInterface{
		fakeIface(t, 1, "eth0", "192.168.4.10/22"),
		fakeIface(t, 2, "docker0", "172.17.0.1/16"),
	}
	if got := onLink(ifaces, net.ParseIP("192.168.5.200")); len(got) != 1 || got[0].Name != "eth0" {
		t.Errorf("192.168.5.200 should be on eth0 (/22), got %v", got)
	}
	if got := onLink(ifaces, net.ParseIP("192.168.0.130")); len(got) != 0 {
		t.Errorf("192.168.0.130 is routed, not on-link; got %v", got)
	}
	if got := onLink(ifaces, net.ParseIP("172.17.9.9")); len(got) != 1 || got[0].Name != "docker0" {
		t.Errorf("172.17.9.9 should be on docker0, got %v", got)
	}
}

func TestSlash24(t *testing.T) {
	// The /24 around an IPv4 address is its x.y.z.0/24 block; an IPv6
	// address has none and gets nil.
	n := slash24(net.ParseIP("192.168.5.77"))
	if n.String() != "192.168.5.0/24" {
		t.Errorf("slash24 = %s, want 192.168.5.0/24", n)
	}
	if slash24(net.ParseIP("fe80::1")) != nil {
		t.Error("slash24 of an IPv6 address should be nil")
	}
}

func TestHosts(t *testing.T) {
	// Host lists drop network and broadcast except on a /31, honor the
	// cap (nil at 0) and stop at the top of the address space instead
	// of wrapping around.
	_, n24, _ := net.ParseCIDR("10.1.2.0/24")
	got := hosts(n24, 1000)
	if len(got) != 254 || got[0].String() != "10.1.2.1" || got[253].String() != "10.1.2.254" {
		t.Errorf("/24 hosts: len=%d first=%v last=%v", len(got), got[0], got[len(got)-1])
	}
	if got := hosts(n24, 10); len(got) != 10 {
		t.Errorf("cap not honored: %d", len(got))
	}
	// /31 and /32 have no network/broadcast to skip.
	_, n31, _ := net.ParseCIDR("10.1.2.0/31")
	if got := hosts(n31, 10); len(got) != 2 {
		t.Errorf("/31 hosts = %v, want both addresses", ipList(got))
	}
	// /30 keeps the two usable hosts.
	_, n30, _ := net.ParseCIDR("10.1.2.0/30")
	if got := hosts(n30, 10); len(got) != 2 || got[0].String() != "10.1.2.1" || got[1].String() != "10.1.2.2" {
		t.Errorf("/30 hosts = %v", ipList(got))
	}
	if got := hosts(n24, 0); got != nil {
		t.Errorf("max=0 should yield nil, got %d", len(got))
	}
	// The top of the address space must not wrap around.
	_, top, _ := net.ParseCIDR("255.255.255.252/30")
	if got := hosts(top, 10); len(got) != 2 {
		t.Errorf("top-of-space /30 = %v", ipList(got))
	}
}

func TestSweepAddresses_HintBlockFirstThenRestOfSubnet(t *testing.T) {
	// A sweep starts with the hint's /24, where a moved camera most
	// likely is, then walks the rest of the /22 with no repeats and
	// never asks about our own address.
	eth := fakeIface(t, 1, "eth0", "192.168.4.10/22") // 192.168.4.0 - 192.168.7.255
	got := sweepAddresses([]lanInterface{eth}, []net.IP{net.ParseIP("192.168.5.123")}, 4096)

	// 253 in the hint /24 (254 minus nothing — our own IP is in .4.x),
	// then the remaining three /24s minus our own address and the
	// subnet's network/broadcast.
	if got[0].String() != "192.168.5.1" {
		t.Errorf("first address = %s, want the hint's /24 to lead", got[0])
	}
	if got[253].String() != "192.168.5.254" || got[254].String() != "192.168.4.1" {
		t.Errorf("hint block should be contiguous then wrap to the subnet start; got [253]=%s [254]=%s", got[253], got[254])
	}
	want := 1022 - 1 // usable /22 hosts minus ourselves
	if len(got) != want {
		t.Errorf("len = %d, want %d", len(got), want)
	}
	seen := make(map[string]bool)
	for _, ip := range got {
		if seen[ip.String()] {
			t.Fatalf("duplicate %s", ip)
		}
		seen[ip.String()] = true
		if ip.Equal(eth.IP) {
			t.Fatalf("our own address %s must never be swept", ip)
		}
	}
}

func TestSweepAddresses_HintOffLinkContributesNothing(t *testing.T) {
	// A hint on no local subnet adds nothing (ARP can't reach it), so the
	// sweep is just the local /24 minus our own address.
	eth := fakeIface(t, 1, "eth0", "192.168.4.10/24")
	got := sweepAddresses([]lanInterface{eth}, []net.IP{net.ParseIP("192.168.0.130")}, 4096)
	if len(got) != 253 {
		t.Errorf("len = %d, want 253 (the local /24 minus ourselves)", len(got))
	}
	if got[0].String() != "192.168.4.1" {
		t.Errorf("first = %s, want 192.168.4.1", got[0])
	}
}

func TestSweepAddresses_WideSubnetOnlyGetsHintBlock(t *testing.T) {
	// A /16 like docker0 is far too big to walk: with no hint it adds
	// nothing, and with a hint inside it only that /24 gets swept.
	big := fakeIface(t, 2, "docker0", "172.17.0.1/16")
	// No hint: a /16 is never walked in full.
	if got := sweepAddresses([]lanInterface{big}, nil, 100000); len(got) != 0 {
		t.Errorf("/16 without a hint should contribute nothing, got %d", len(got))
	}
	// With a hint inside it: just that /24.
	got := sweepAddresses([]lanInterface{big}, []net.IP{net.ParseIP("172.17.40.9")}, 100000)
	if len(got) != 254 || got[0].String() != "172.17.40.1" {
		t.Errorf("hint /24 inside a /16: len=%d first=%v", len(got), got[0])
	}
}

func TestSweepAddresses_CapAndNilHints(t *testing.T) {
	// A nil hint is skipped, and the cap cuts the list off while the real
	// hint's /24 still comes first.
	eth := fakeIface(t, 1, "eth0", "192.168.4.10/22")
	got := sweepAddresses([]lanInterface{eth}, []net.IP{nil, net.ParseIP("192.168.6.2")}, 10)
	if len(got) != 10 || got[0].String() != "192.168.6.1" {
		t.Errorf("cap=10 with hint: len=%d first=%v", len(got), got[0])
	}
}

func TestSweepAddresses_TwoInterfacesNoDuplicates(t *testing.T) {
	// Two NICs on the same subnet don't sweep it twice, and neither of
	// our own addresses is in the list.
	a := fakeIface(t, 1, "eth0", "10.0.0.2/24")
	b := fakeIface(t, 2, "wlan0", "10.0.0.3/24") // same subnet, second NIC
	got := sweepAddresses([]lanInterface{a, b}, []net.IP{net.ParseIP("10.0.0.50")}, 1000)
	if len(got) != 252 { // 254 minus both of our addresses
		t.Errorf("len = %d, want 252", len(got))
	}
}

func TestListInterfaces_Real(t *testing.T) {
	// On the real host, each entry has a MAC, an IPv4 address, a subnet
	// and an index, and loopback and link-local addresses are left out.
	ifs, err := listInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ifi := range ifs {
		if len(ifi.HW) != 6 || ifi.IP.To4() == nil || ifi.Net == nil || ifi.Index == 0 {
			t.Errorf("malformed interface entry: %+v", ifi)
		}
		if ifi.IP.IsLoopback() || ifi.IP.IsLinkLocalUnicast() {
			t.Errorf("loopback/link-local should be filtered: %+v", ifi)
		}
	}
}
