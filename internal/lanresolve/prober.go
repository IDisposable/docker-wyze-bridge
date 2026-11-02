package lanresolve

import (
	"context"
	"errors"
	"net"
	"time"
)

// prober is the only thing the resolver needs from the network: who
// answers for an address, and which address a given hardware address
// answers from. Two engines implement it. The ARP engine (Linux, needs
// CAP_NET_RAW, which Docker grants by default) sends its own ARP
// requests and reads the replies, so every answer is live. The neighbor
// engine falls back to the kernel's own cache when raw sockets aren't
// allowed; it can lag a stale entry's lifetime behind reality, which the
// resolver tolerates by re-checking periodically.
type prober interface {
	// probe returns the normalized MAC currently answering for ip, or ""
	// when nothing answered within the engine's timeout. want is the
	// MAC the caller expects there (normalized, or ""): an engine that
	// can address frames directly uses it, since a Wi-Fi camera dozing
	// in power save can miss broadcasts for seconds but gets a unicast
	// frame delivered and retried by its access point. An error means
	// the question couldn't be asked at all (off-link address, socket
	// failure) rather than "nobody home".
	probe(ctx context.Context, ip net.IP, want string) (string, error)

	// sweep asks every address in ips, in order, and returns the first
	// one claimed by mac, or nil when none was. heard carries every
	// MAC -> IP reply observed along the way. Engines pace their sends
	// so a /22 doesn't turn into a storm on Wi-Fi.
	sweep(ctx context.Context, ips []net.IP, mac string) (found net.IP, heard map[string]net.IP, err error)

	// name labels the engine in logs and the metrics page.
	name() string
}

// errOffLink is returned by probe when no local interface shares a
// subnet with the address: ARP cannot reach it, so there is no point
// waiting for a reply.
var errOffLink = errors.New("address is not on any local subnet")

// Timing shared by both engines. A camera on Wi-Fi power save wakes
// for its access point only every listen interval, so the probe
// retransmits and waits a second and a half before giving up; a healthy
// camera answers in a few milliseconds and never sees the retransmit.
const (
	probeTimeout    = 1500 * time.Millisecond
	probeRetransmit = 250 * time.Millisecond
	sweepTail       = 1000 * time.Millisecond // listen after the last request
)

// newProber picks the best engine the host allows. The ARP engine is
// tried first; when it reports it can't open a packet socket the
// neighbor-cache engine takes over. nil means neither works (no IPv4
// interfaces, or a platform with no neighbor table access), in which
// case the resolver hands back the cloud IP untouched.
func newProber(ifaces func() ([]lanInterface, error)) (prober, error) {
	if eng, err := newARPEngine(ifaces); err == nil {
		return eng, nil
	} else if !errors.Is(err, errARPUnavailable) {
		return nil, err
	}
	if eng, err := newNeighEngine(ifaces); err == nil {
		return eng, nil
	} else {
		return nil, err
	}
}

// udpNudge sends one byte to UDP port 9 (discard) on ip. The payload is
// irrelevant: the point is to make the kernel resolve the address, which
// it does by broadcasting an ARP request and recording the answer in its
// neighbor cache. A device at that address drops the datagram or
// answers with an ICMP port-unreachable; either way nothing on the
// camera notices. Used by the neighbor engine, which can't ARP itself.
func udpNudge(ctx context.Context, ip net.IP) error {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(ip.String(), "9"))
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte{0})
	return err
}
