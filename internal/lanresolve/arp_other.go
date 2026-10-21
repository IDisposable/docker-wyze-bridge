//go:build !linux

package lanresolve

import "errors"

// errARPUnavailable tells newProber to fall back to the neighbor engine.
var errARPUnavailable = errors.New("raw ARP unavailable")

// newARPEngine is Linux-only: AF_PACKET sockets don't exist elsewhere.
// Developers on macOS/Windows get the neighbor engine (or no engine)
// and the resolver degrades to the cloud IP, which is today's behavior.
func newARPEngine(func() ([]lanInterface, error)) (prober, error) {
	return nil, errARPUnavailable
}
