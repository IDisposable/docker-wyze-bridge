//go:build linux

package lanresolve

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"
)

// arpEngine asks the LAN directly. It opens an AF_PACKET socket per
// interface, sends ARP requests with the kernel adding the Ethernet
// header, and reads the replies itself — nothing touches the kernel's
// neighbor cache, so a stale entry there can't mislead us. This is the
// same thing arp-scan does, and it's what makes a wrong cloud IP cost a
// few milliseconds instead of go2rtc's five-second discovery timeout.
//
// Requests for a camera whose MAC we know are sent unicast to that MAC
// (a broadcast copy goes out too on a probe, so a stranger holding the
// address still shows up). Wi-Fi matters here: a broadcast frame is
// transmitted once, unacknowledged, and a camera dozing in power save
// may sleep through it, whereas a unicast frame is queued for the
// station, acknowledged and retried by the access point. Wyze cameras
// answered broadcast probes at startup and then went quiet for the
// kernel's own broadcast ARP a minute later; unicast reaches them.
//
// The kernel matters too, because go2rtc's packets leave through its
// neighbor cache, and the kernel only resolves a new address by
// broadcast — the same broadcast the camera sleeps through, which is
// how "discovery timeout" happens for a camera that is right there.
// The kernel does record an ARP reply for an entry it already has, so
// a probe first sends the address a throwaway UDP datagram (creating
// or refreshing the entry) and then the unicast request; the camera's
// reply lands in that entry and go2rtc's next dial goes straight out.
type arpEngine struct {
	ifaces func() ([]lanInterface, error)

	// sendPace is the gap between consecutive requests during a sweep.
	// Broadcast frames go out at the Wi-Fi basic rate, so a /22 at full
	// speed would hog the air; 2ms keeps a /24 under half a second.
	sendPace time.Duration
	// recvSlice bounds each blocking read so a listener can notice the
	// context is done; it is latency the caller never sees because
	// replies are delivered through a channel as they arrive.
	recvSlice time.Duration
	// nudge sends the kernel-seeding datagram (udpNudge); a seam for
	// tests.
	nudge func(ctx context.Context, ip net.IP) error
}

// errARPUnavailable tells newProber to fall back to the neighbor engine.
// Raised when the packet socket is refused (no CAP_NET_RAW) or there is
// no interface to bind it to.
var errARPUnavailable = errors.New("raw ARP unavailable")

// errNoSocket means every interface that could reach the address
// refused a packet socket at probe time (permissions changed, or the
// interface went away between enumeration and bind). Distinguished
// from "nobody answered" so the resolver logs it instead of treating
// the camera as absent.
var errNoSocket = errors.New("no ARP socket could be opened")

// newARPEngine checks that a packet socket can actually be opened before
// committing to this engine, so a container without CAP_NET_RAW degrades
// at startup rather than on the first camera.
func newARPEngine(ifaces func() ([]lanInterface, error)) (prober, error) {
	list, err := ifaces()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: no LAN interfaces", errARPUnavailable)
	}
	s, err := openARPSocket(list[0])
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("%w: %v", errARPUnavailable, err)
		}
		return nil, err
	}
	s.close()
	return &arpEngine{
		ifaces:    ifaces,
		sendPace:  2 * time.Millisecond,
		recvSlice: 100 * time.Millisecond,
		nudge:     udpNudge,
	}, nil
}

func (e *arpEngine) name() string { return "arp" }

// probe sends a request for ip on every interface that shares its subnet
// and returns the first reply's hardware address. Retransmits every
// probeRetransmit until probeTimeout or ctx is done. With want set,
// each round also sends the request unicast to that MAC (see the type
// comment for why).
func (e *arpEngine) probe(ctx context.Context, ip net.IP, want string) (string, error) {
	ifaces, err := e.ifaces()
	if err != nil {
		return "", err
	}
	targets := onLink(ifaces, ip)
	if len(targets) == 0 {
		return "", errOffLink
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	socks, replies, done := e.listen(ctx, targets)
	defer done()
	if len(socks) == 0 {
		return "", errNoSocket
	}

	direct := macBytes(want)
	if direct != nil {
		// Give the kernel an entry for the reply to land in (see the
		// type comment). Best effort: a failed nudge is just a nudge
		// not sent.
		_ = e.nudge(ctx, ip)
	}
	retry := time.NewTicker(probeRetransmit)
	defer retry.Stop()
	for {
		for _, s := range socks {
			// A failed send on one interface shouldn't stop the others.
			_ = s.send(ip, broadcastHW)
			if direct != nil {
				_ = s.send(ip, direct)
			}
		}
	wait:
		for {
			select {
			case r := <-replies:
				if r.SenderIP.Equal(ip) {
					return macString(r.SenderHW), nil
				}
				// Somebody else's ARP traffic; keep waiting.
			case <-retry.C:
				break wait
			case <-ctx.Done():
				return "", nil
			}
		}
	}
}

// sweep paces one request per address, listening the whole time, and
// returns as soon as mac claims any of them. After the last request it
// keeps listening for sweepTail so a slow reply to an early request
// still counts. Every reply is collected into heard. The requests go
// unicast to mac: only that station can answer, the switch delivers
// them to its port alone, and the access point retries them, so the
// sweep is both reliable for a dozing camera and nearly free for
// everyone else on the LAN. A MAC that doesn't parse falls back to
// broadcast.
func (e *arpEngine) sweep(ctx context.Context, ips []net.IP, mac string) (net.IP, map[string]net.IP, error) {
	ifaces, err := e.ifaces()
	if err != nil {
		return nil, nil, err
	}
	// One socket per interface that has at least one address to ask.
	byIndex := make(map[int]lanInterface)
	for _, ip := range ips {
		for _, ifi := range onLink(ifaces, ip) {
			byIndex[ifi.Index] = ifi
		}
	}
	if len(byIndex) == 0 {
		return nil, nil, errOffLink
	}
	targets := make([]lanInterface, 0, len(byIndex))
	for _, ifi := range byIndex {
		targets = append(targets, ifi)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	socks, replies, done := e.listen(ctx, targets)
	defer done()
	if len(socks) == 0 {
		return nil, nil, errNoSocket
	}
	sockByIndex := make(map[int]*arpSocket, len(socks))
	for _, s := range socks {
		sockByIndex[s.ifi.Index] = s
	}

	heard := make(map[string]net.IP)
	note := func(r arpReply) bool {
		got := macString(r.SenderHW)
		heard[got] = r.SenderIP
		return got == mac
	}

	dst := macBytes(mac)
	if dst == nil {
		dst = broadcastHW
	}
	pace := time.NewTicker(e.sendPace)
	defer pace.Stop()
	for _, ip := range ips {
		for _, ifi := range onLink(ifaces, ip) {
			if s := sockByIndex[ifi.Index]; s != nil {
				_ = s.send(ip, dst)
			}
		}
		select {
		case r := <-replies:
			if note(r) {
				return r.SenderIP, heard, nil
			}
		case <-pace.C:
		case <-ctx.Done():
			return nil, heard, nil
		}
	}
	tail := time.NewTimer(sweepTail)
	defer tail.Stop()
	for {
		select {
		case r := <-replies:
			if note(r) {
				return r.SenderIP, heard, nil
			}
		case <-tail.C:
			return nil, heard, nil
		case <-ctx.Done():
			return nil, heard, nil
		}
	}
}

// listen opens a socket per interface and starts a reader goroutine on
// each that forwards ARP replies into one channel. The returned done
// func stops the readers and closes the sockets; it returns immediately
// and lets the readers finish their current timeslice in the background
// so callers don't pay the recvSlice latency on every probe.
func (e *arpEngine) listen(ctx context.Context, ifaces []lanInterface) ([]*arpSocket, <-chan arpReply, func()) {
	ctx, cancel := context.WithCancel(ctx)
	replies := make(chan arpReply, 64)
	var socks []*arpSocket
	var wg sync.WaitGroup
	for _, ifi := range ifaces {
		s, err := openARPSocket(ifi)
		if err != nil {
			continue // probe/sweep still work on the interfaces that opened
		}
		socks = append(socks, s)
		wg.Add(1)
		go func(s *arpSocket) {
			defer wg.Done()
			buf := make([]byte, 128)
			for ctx.Err() == nil {
				n, err := s.recv(buf, e.recvSlice)
				if err != nil {
					continue // timeout or transient error: re-check ctx and read again
				}
				if r, ok := parseARPReply(buf[:n]); ok {
					select {
					case replies <- r:
					default: // reader is behind; dropping ARP noise is fine
					}
				}
			}
		}(s)
	}
	done := func() {
		cancel()
		go func() {
			wg.Wait()
			for _, s := range socks {
				s.close()
			}
		}()
	}
	return socks, replies, done
}

// arpSocket is one AF_PACKET/SOCK_DGRAM socket bound to an interface
// for ETH_P_ARP. SOCK_DGRAM (rather than SOCK_RAW) means the kernel
// builds and strips the Ethernet header, so the payload we send and
// receive is the bare 28-byte ARP packet.
type arpSocket struct {
	fd  int
	ifi lanInterface
}

func openARPSocket(ifi lanInterface) (*arpSocket, error) {
	proto := htons(syscall.ETH_P_ARP)
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, int(proto))
	if err != nil {
		return nil, fmt.Errorf("packet socket: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: ifi.Index}); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind %s: %w", ifi.Name, err)
	}
	return &arpSocket{fd: fd, ifi: ifi}, nil
}

// send broadcasts a who-has request for target from this interface's
// own address.
// broadcastHW is the Ethernet destination for a classic ARP request.
var broadcastHW = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// send writes one "who has target" request to the interface, addressed
// at the Ethernet layer to dst: broadcastHW to ask everyone, or a
// station's own MAC to ask it alone.
func (s *arpSocket) send(target net.IP, dst net.HardwareAddr) error {
	to := &syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ARP),
		Ifindex:  s.ifi.Index,
		Halen:    6,
	}
	copy(to.Addr[:], dst)
	pkt := arpRequest(s.ifi.HW, s.ifi.IP, target)
	return syscall.Sendto(s.fd, pkt, 0, to)
}

// recv blocks for at most timeout. Timeouts and interrupted reads come
// back as errors the listener simply retries.
func (s *arpSocket) recv(buf []byte, timeout time.Duration) (int, error) {
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	if err := syscall.SetsockoptTimeval(s.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return 0, err
	}
	n, _, err := syscall.Recvfrom(s.fd, buf, 0)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *arpSocket) close() {
	_ = syscall.Close(s.fd)
}

// htons converts a 16-bit value to network byte order as the packet
// socket API expects it in sockaddr_ll.sll_protocol.
func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return binary.NativeEndian.Uint16(b[:])
}
