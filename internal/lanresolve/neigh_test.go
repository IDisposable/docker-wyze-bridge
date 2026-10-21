package lanresolve

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// ndmsg assembles an RTM_NEWNEIGH payload the way the kernel lays it
// out, so parseNeighbor is tested against the real wire format rather
// than a convenient one.
type ndmsgOpts struct {
	family  byte
	ifindex int32
	state   uint16
	flags   byte
	ip      net.IP           // nil: omit NDA_DST
	mac     net.HardwareAddr // nil: omit NDA_LLADDR
	updated uint32           // ticks; -1 via noCache to omit NDA_CACHEINFO
	noCache bool
	extra   []byte // raw bytes appended after the known attrs
}

func rtattr(typ uint16, payload []byte) []byte {
	l := rtaHdr + len(payload)
	b := make([]byte, (l+rtaAlign-1)&^(rtaAlign-1))
	binary.NativeEndian.PutUint16(b[0:2], uint16(l))
	binary.NativeEndian.PutUint16(b[2:4], typ)
	copy(b[rtaHdr:], payload)
	return b
}

func ndmsg(o ndmsgOpts) []byte {
	b := make([]byte, ndmsgLen)
	if o.family == 0 {
		o.family = 2 // AF_INET
	}
	b[0] = o.family
	binary.NativeEndian.PutUint32(b[4:8], uint32(o.ifindex))
	binary.NativeEndian.PutUint16(b[8:10], o.state)
	b[10] = o.flags
	if o.ip != nil {
		b = append(b, rtattr(ndaDst, o.ip.To4())...)
	}
	if o.mac != nil {
		b = append(b, rtattr(ndaLLAddr, o.mac)...)
	}
	if !o.noCache {
		ci := make([]byte, 16)
		binary.NativeEndian.PutUint32(ci[8:12], o.updated)
		b = append(b, rtattr(ndaCacheInfo, ci)...)
	}
	return append(b, o.extra...)
}

var camHW = net.HardwareAddr{0xd0, 0x3f, 0x27, 0x00, 0xca, 0x04}

func TestParseNeighbor_Reachable(t *testing.T) {
	// A normal REACHABLE entry parses with its IP, MAC and interface, its
	// age comes from USER_HZ ticks, and it counts as live and known.
	n, ok := parseNeighbor(ndmsg(ndmsgOpts{
		ifindex: 3, state: nudReachable, ip: net.ParseIP("192.168.4.151"), mac: camHW, updated: 250,
	}))
	if !ok {
		t.Fatal("rejected a normal entry")
	}
	if n.IP.String() != "192.168.4.151" || n.MAC != "d03f2700ca04" || n.Ifindex != 3 {
		t.Errorf("parsed %+v", n)
	}
	if n.Age != 2500*time.Millisecond {
		t.Errorf("age = %v, want 2.5s (250 ticks at USER_HZ=100)", n.Age)
	}
	if !n.live() || !n.known() {
		t.Error("REACHABLE should be live and known")
	}
}

func TestParseNeighbor_States(t *testing.T) {
	// Each kernel state maps to the right live/known pair: STALE is known
	// but not live, INCOMPLETE and FAILED are neither, and a zero MAC is
	// never trusted even when the entry says REACHABLE.
	cases := []struct {
		name        string
		state       uint16
		mac         net.HardwareAddr
		live, known bool
	}{
		{"stale", nudStale, camHW, false, true},
		{"delay", nudDelay, camHW, true, true},
		{"probe", nudProbe, camHW, true, true},
		{"permanent", nudPermanent, camHW, true, true},
		{"noarp", nudNoARP, camHW, true, true},
		{"incomplete", nudIncomplete, nil, false, false},
		{"failed", nudFailed, nil, false, false},
		{"reachable but zero mac", nudReachable, net.HardwareAddr{0, 0, 0, 0, 0, 0}, false, false},
	}
	for _, c := range cases {
		n, ok := parseNeighbor(ndmsg(ndmsgOpts{state: c.state, ip: net.ParseIP("10.0.0.5"), mac: c.mac}))
		if !ok {
			t.Errorf("%s: rejected", c.name)
			continue
		}
		if n.live() != c.live || n.known() != c.known {
			t.Errorf("%s: live=%v known=%v, want %v/%v", c.name, n.live(), n.known(), c.live, c.known)
		}
	}
}

func TestParseNeighbor_Rejects(t *testing.T) {
	// IPv6 and proxy entries, entries with no address, and short or
	// malformed payloads are rejected rather than half parsed.
	ip := net.ParseIP("10.0.0.5")
	cases := map[string][]byte{
		"ipv6":          ndmsg(ndmsgOpts{family: 10, state: nudReachable, ip: ip, mac: camHW}),
		"proxy":         ndmsg(ndmsgOpts{flags: ntfProxy, state: nudReachable, ip: ip, mac: camHW}),
		"no dst":        ndmsg(ndmsgOpts{state: nudReachable, mac: camHW}),
		"short":         make([]byte, ndmsgLen-1),
		"empty":         nil,
		"malformed rta": ndmsg(ndmsgOpts{state: nudReachable, mac: camHW, extra: []byte{0xff, 0xff, 0x01, 0x00}}),
	}
	for name, b := range cases {
		if _, ok := parseNeighbor(b); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseNeighbor_ToleratesUnknownAndMissingAttrs(t *testing.T) {
	// An unknown attribute (NDA_PROBES=4) between the known ones, and no
	// cacheinfo at all: still parses, age simply reads as zero.
	b := ndmsg(ndmsgOpts{state: nudStale, ip: net.ParseIP("10.0.0.9"), noCache: true})
	b = append(b, rtattr(4, []byte{1, 0, 0, 0})...)
	b = append(b, rtattr(ndaLLAddr, camHW)...)
	n, ok := parseNeighbor(b)
	if !ok || n.MAC != "d03f2700ca04" || n.Age != 0 {
		t.Errorf("parsed ok=%v %+v", ok, n)
	}
	// A truncated final attribute: keep what came before it.
	b = ndmsg(ndmsgOpts{state: nudReachable, ip: net.ParseIP("10.0.0.9"), mac: camHW})
	b = append(b, 0x10, 0x00, 0x03, 0x00, 0x01) // claims 16 bytes, has 5
	n, ok = parseNeighbor(b)
	if !ok || n.MAC != "d03f2700ca04" {
		t.Errorf("truncated trailing attr: ok=%v %+v", ok, n)
	}
}

func TestFindByIP(t *testing.T) {
	// Looking up by IP returns the matching entry, and an address the
	// cache doesn't have is reported as a miss.
	entries := []neighbor{
		{IP: net.ParseIP("10.0.0.1").To4(), MAC: "aaaaaaaaaaaa", State: nudReachable},
		{IP: net.ParseIP("10.0.0.2").To4(), MAC: "bbbbbbbbbbbb", State: nudStale},
	}
	if n, ok := findByIP(entries, net.ParseIP("10.0.0.2")); !ok || n.MAC != "bbbbbbbbbbbb" {
		t.Errorf("findByIP = %+v, %v", n, ok)
	}
	if _, ok := findByIP(entries, net.ParseIP("10.0.0.3")); ok {
		t.Error("absent address found")
	}
}

func TestFindByMAC_LiveBeforeStaleThenYoungest(t *testing.T) {
	// A MAC's entries come back live first, then the youngest stale ones,
	// which is the order the resolver probes them in. Other MACs and
	// FAILED entries are left out.
	mac := "d03f2700ca04"
	entries := []neighbor{
		{IP: net.ParseIP("10.0.0.123").To4(), MAC: mac, State: nudStale, Age: 10 * time.Second},     // old address
		{IP: net.ParseIP("10.0.0.7").To4(), MAC: "ffffffffffff", State: nudReachable},               // someone else
		{IP: net.ParseIP("10.0.0.151").To4(), MAC: mac, State: nudReachable, Age: 90 * time.Second}, // new address, live
		{IP: net.ParseIP("10.0.0.140").To4(), MAC: mac, State: nudStale, Age: 3 * time.Second},      // another stale, younger
		{IP: net.ParseIP("10.0.0.9").To4(), MAC: mac, State: nudFailed},                             // no address: ignored
	}
	got := findByMAC(entries, mac)
	want := []string{"10.0.0.151", "10.0.0.140", "10.0.0.123"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].IP.String() != w {
			t.Errorf("[%d] = %s, want %s", i, got[i].IP, w)
		}
	}
	if len(findByMAC(entries, "000000000000")) != 0 {
		t.Error("unknown MAC should match nothing")
	}
}
