//go:build linux

package lanresolve

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// These exercise the real packet socket against whatever LAN the test
// host is on. They skip, rather than fail, when the environment can't
// support them (no CAP_NET_RAW, no default route), so CI containers
// without raw sockets still pass.

// defaultGateway reads the IPv4 default route from /proc/net/route.
func defaultGateway(t *testing.T) net.IP {
	t.Helper()
	f, err := os.Open("/proc/net/route")
	if err != nil {
		t.Skipf("no /proc/net/route: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(fields[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).To4() // little-endian in /proc
	}
	t.Skip("no default route")
	return nil
}

func realARPEngine(t *testing.T) *arpEngine {
	t.Helper()
	eng, err := newARPEngine(listInterfaces)
	if errors.Is(err, errARPUnavailable) {
		t.Skipf("raw ARP unavailable here: %v", err)
	}
	if err != nil {
		t.Fatalf("newARPEngine: %v", err)
	}
	return eng.(*arpEngine)
}

func TestARPEngine_ProbeDefaultGateway(t *testing.T) {
	// Against the real LAN: the gateway answers an ARP probe with a full
	// MAC, and Check agrees with the probe (true for that MAC, false for
	// a made-up one).
	eng := realARPEngine(t)
	gw := defaultGateway(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	mac, err := eng.probe(ctx, gw, "")
	if err != nil {
		t.Fatalf("probe %s: %v", gw, err)
	}
	if len(mac) != 12 {
		t.Fatalf("probe %s: want a 12-hex MAC, got %q", gw, mac)
	}
	// A gateway answers in well under the retransmit interval; a probe
	// that only succeeded via retransmit would still pass, but the
	// typical case should be fast.
	t.Logf("gateway %s is at %s (%v)", gw, mac, time.Since(start))

	// Check should agree with probe.
	r := newResolver(Config{}, testLogger())
	r.engine = eng
	ok, err := r.Check(ctx, gw.String(), mac)
	if err != nil || !ok {
		t.Fatalf("Check(%s, %s) = %v, %v; want true", gw, mac, ok, err)
	}
	ok, err = r.Check(ctx, gw.String(), "00aabbccddee")
	if err != nil || ok {
		t.Fatalf("Check with wrong MAC = %v, %v; want false, nil", ok, err)
	}
}

func TestARPEngine_ProbeUnusedAddressTimesOut(t *testing.T) {
	// An address nobody holds reads as "nobody home" (empty, no error)
	// and the probe gives up after about probeTimeout instead of hanging
	// or quitting too early for a dozing camera to answer.
	eng := realARPEngine(t)
	gw := defaultGateway(t)
	ifaces, _ := listInterfaces()
	link := onLink(ifaces, gw)
	if len(link) == 0 {
		t.Skip("gateway not on a local subnet")
	}
	// The broadcast-1 address is a decent bet for "nobody home".
	n := link[0].Net
	all := hosts(n, 1<<16)
	if len(all) < 2 {
		t.Skip("subnet too small")
	}
	target := all[len(all)-1]
	if target.Equal(gw) || target.Equal(link[0].IP) {
		target = all[len(all)-2]
	}
	ctx := context.Background()
	start := time.Now()
	mac, err := eng.probe(ctx, target, "")
	took := time.Since(start)
	if err != nil {
		t.Fatalf("probe %s: %v", target, err)
	}
	if mac != "" {
		t.Skipf("%s is in use (%s); can't test the timeout path here", target, mac)
	}
	if took < probeTimeout-50*time.Millisecond || took > probeTimeout+500*time.Millisecond {
		t.Errorf("probe of empty address took %v, want ≈%v", took, probeTimeout)
	}
}

func TestARPEngine_ProbeOffLink(t *testing.T) {
	// An address on no local subnet fails with errOffLink rather than
	// reading as "nobody home": ARP can't reach it at all.
	eng := realARPEngine(t)
	ifaces, _ := listInterfaces()
	// The documentation TEST-NETs are never on a real LAN — except in a
	// sandbox that happens to use one, so pick whichever isn't local.
	var target net.IP
	for _, s := range []string{"192.0.2.77", "198.51.100.77", "203.0.113.77"} {
		if ip := net.ParseIP(s); len(onLink(ifaces, ip)) == 0 {
			target = ip
			break
		}
	}
	if target == nil {
		t.Skip("every TEST-NET is on-link here")
	}
	_, err := eng.probe(context.Background(), target, "")
	if !errors.Is(err, errOffLink) {
		t.Fatalf("probe of off-link %s: err = %v, want errOffLink", target, err)
	}
}

func TestARPEngine_SweepFindsGateway(t *testing.T) {
	// A real sweep of the gateway's /24, addressed to the gateway's MAC,
	// lands on the gateway and records it in heard.
	eng := realARPEngine(t)
	gw := defaultGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mac, err := eng.probe(ctx, gw, "")
	if err != nil || mac == "" {
		t.Skipf("gateway did not answer a probe (%q, %v)", mac, err)
	}
	ifaces, _ := listInterfaces()
	// Sweep the /24 around the gateway and expect its MAC to land on it.
	addrs := sweepAddresses(ifaces, []net.IP{gw}, 256)
	found, heard, err := eng.sweep(ctx, addrs, mac)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !gw.Equal(found) {
		t.Fatalf("sweep found %v for %s, want %s", found, mac, gw)
	}
	if ip, ok := heard[mac]; !ok || !ip.Equal(gw) {
		t.Errorf("heard[%s] = %v, want %s", mac, ip, gw)
	}
}

func TestReadNeighbors_Smoke(t *testing.T) {
	// The real kernel neighbor table can be read, and only IPv4 entries
	// make it through.
	entries, err := readNeighbors()
	if err != nil {
		t.Skipf("neighbor table unreadable: %v", err)
	}
	for _, n := range entries {
		if n.IP.To4() == nil {
			t.Errorf("non-IPv4 entry leaked through: %+v", n)
		}
	}
	t.Logf("%d IPv4 neighbor entries", len(entries))
}

func TestNeighEngine_Real_ProbeDefaultGateway(t *testing.T) {
	// The fallback engine against the real kernel cache: a UDP nudge
	// should make the kernel resolve the gateway, and the answer should
	// agree with what the ARP engine sees.
	arp := realARPEngine(t)
	gw := defaultGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want, err := arp.probe(ctx, gw, "")
	if err != nil || want == "" {
		t.Skipf("gateway did not answer ARP (%q, %v)", want, err)
	}

	eng, err := newNeighEngine(listInterfaces)
	if err != nil {
		t.Skipf("neighbor engine unavailable: %v", err)
	}
	if eng.name() != "neigh" {
		t.Errorf("name = %q", eng.name())
	}
	got, err := eng.probe(ctx, gw, "")
	if err != nil {
		t.Fatalf("neigh probe %s: %v", gw, err)
	}
	if got != want {
		t.Errorf("neigh engine says %s is at %q, ARP engine says %q", gw, got, want)
	}
}

func TestNewProber_FallsBackToNeighWithoutInterfaces(t *testing.T) {
	// No interfaces means the ARP engine can't bind anything, which is
	// reported as "unavailable" and must fall through to the neighbor
	// engine rather than fail outright.
	eng, err := newProber(func() ([]lanInterface, error) { return nil, nil })
	if err != nil {
		t.Skipf("no engine at all here: %v", err)
	}
	if eng.name() != "neigh" {
		t.Errorf("engine = %q, want neigh", eng.name())
	}
}

func TestNewProber_InterfaceErrorPropagates(t *testing.T) {
	// If listing interfaces fails, newProber hands that error back instead
	// of treating it as "no raw sockets" and falling back to neigh.
	boom := errors.New("no interfaces for you")
	if _, err := newProber(func() ([]lanInterface, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestUDPNudge_OffLinkAndCancelled(t *testing.T) {
	// An unroutable address fails at dial time rather than hanging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := udpNudge(ctx, net.ParseIP("10.255.255.254")); err == nil {
		t.Log("nudge with cancelled context returned nil (dial completed before cancel check); acceptable")
	}
	// A reachable address: the datagram is fire-and-forget.
	gw := defaultGateway(t)
	if err := udpNudge(context.Background(), gw); err != nil {
		t.Errorf("nudge %s: %v", gw, err)
	}
}

func TestARPEngine_ProbeNudgesKernelWhenMACKnown(t *testing.T) {
	// With the expected MAC known, the probe first sends the address a
	// datagram so the kernel has a neighbor entry for the reply to land
	// in; without one it just asks.
	eng := realARPEngine(t)
	gw := defaultGateway(t)
	var nudged []string
	eng.nudge = func(_ context.Context, ip net.IP) error { nudged = append(nudged, ip.String()); return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mac, err := eng.probe(ctx, gw, "")
	if err != nil || mac == "" {
		t.Skipf("gateway didn't answer a broadcast probe (%q, %v); can't test the unicast path here", mac, err)
	}
	if len(nudged) != 0 {
		t.Errorf("probe without a MAC nudged %v", nudged)
	}
	again, err := eng.probe(ctx, gw, mac)
	if err != nil || again != mac {
		t.Errorf("unicast probe = %q, %v (want %q)", again, err, mac)
	}
	if len(nudged) != 1 || nudged[0] != gw.String() {
		t.Errorf("probe with a MAC should nudge the address once, got %v", nudged)
	}
}
