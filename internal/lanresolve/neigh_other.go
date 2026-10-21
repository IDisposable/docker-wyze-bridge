//go:build !linux

package lanresolve

import "errors"

// errNoNeighborTable means this platform offers no neighbor cache the
// resolver knows how to read. The resolver then has no engine and
// leaves cloud IPs alone.
var errNoNeighborTable = errors.New("neighbor table not available on this platform")

func readNeighbors() ([]neighbor, error) {
	return nil, errNoNeighborTable
}
