package lanresolve

import (
	"context"
	"net"
	"time"
)

// neighEngine is the fallback for hosts that refuse raw sockets. It
// can't send ARP itself, so it makes the kernel do it: a throwaway UDP
// datagram to the address forces a lookup, and the answer lands in the
// neighbor cache where we read it back. Two things make it weaker than
// the ARP engine. A STALE entry is taken at face value because forcing
// the kernel to re-verify one takes several seconds (delay_first_probe
// _time), and a sweep leaves a trail of INCOMPLETE entries, so it goes
// in small chunks with pauses. Both are documented in README.
type neighEngine struct {
	ifaces func() ([]lanInterface, error)
	read   func() ([]neighbor, error)
	nudge  func(ctx context.Context, ip net.IP) error

	poll       time.Duration // how often to re-read the cache while waiting
	chunk      int           // addresses nudged per sweep step
	chunkPause time.Duration // settle time after each chunk
}

func newNeighEngine(ifaces func() ([]lanInterface, error)) (prober, error) {
	// Prove the table is readable before committing to this engine.
	if _, err := readNeighbors(); err != nil {
		return nil, err
	}
	return &neighEngine{
		ifaces:     ifaces,
		read:       readNeighbors,
		nudge:      udpNudge,
		poll:       25 * time.Millisecond,
		chunk:      64,
		chunkPause: 200 * time.Millisecond,
	}, nil
}

func (e *neighEngine) name() string { return "neigh" }

// probe returns the cached MAC for ip when the kernel has one, nudging
// the address first if the cache has nothing usable and then polling
// until the kernel's own ARP completes or probeTimeout passes.
func (e *neighEngine) probe(ctx context.Context, ip net.IP) (string, error) {
	ifaces, err := e.ifaces()
	if err != nil {
		return "", err
	}
	if len(onLink(ifaces, ip)) == 0 {
		return "", errOffLink
	}
	entries, err := e.read()
	if err != nil {
		return "", err
	}
	if n, ok := findByIP(entries, ip); ok && n.known() {
		return n.MAC, nil
	}
	if err := e.nudge(ctx, ip); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	tick := time.NewTicker(e.poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", nil
		case <-tick.C:
			entries, err := e.read()
			if err != nil {
				return "", err
			}
			n, ok := findByIP(entries, ip)
			if !ok {
				continue
			}
			if n.live() {
				return n.MAC, nil
			}
			if n.State&nudFailed != 0 {
				return "", nil // the kernel gave up: nobody answered
			}
		}
	}
}

// sweep nudges addresses in chunks and looks for mac in the cache after
// each chunk, stopping at the first hit. heard is every live entry the
// cache held at the end, which covers whatever the nudges resolved.
func (e *neighEngine) sweep(ctx context.Context, ips []net.IP, mac string) (net.IP, map[string]net.IP, error) {
	ifaces, err := e.ifaces()
	if err != nil {
		return nil, nil, err
	}
	var onlink []net.IP
	for _, ip := range ips {
		if len(onLink(ifaces, ip)) > 0 {
			onlink = append(onlink, ip)
		}
	}
	if len(onlink) == 0 {
		return nil, nil, errOffLink
	}
	heard := make(map[string]net.IP)
	collect := func(entries []neighbor) net.IP {
		var found net.IP
		for _, n := range entries {
			if !n.live() {
				continue
			}
			heard[n.MAC] = n.IP
			if n.MAC == mac && found == nil {
				found = n.IP
			}
		}
		return found
	}
	for start := 0; start < len(onlink); start += e.chunk {
		end := min(start+e.chunk, len(onlink))
		for _, ip := range onlink[start:end] {
			if ctx.Err() != nil {
				return nil, heard, nil
			}
			_ = e.nudge(ctx, ip) // an unreachable address is a non-answer, not a failure
		}
		select {
		case <-ctx.Done():
			return nil, heard, nil
		case <-time.After(e.chunkPause):
		}
		entries, err := e.read()
		if err != nil {
			return nil, heard, err
		}
		if found := collect(entries); found != nil {
			return found, heard, nil
		}
	}
	return nil, heard, nil
}
