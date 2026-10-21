//go:build linux

package lanresolve

import (
	"syscall"
)

// readNeighbors dumps the IPv4 neighbor table over netlink. This is an
// unprivileged read — any process can do it — and with network_mode:
// host it's the host's table, which is the one that knows about the
// cameras. The stdlib's syscall package covers the dump request; only
// the per-entry decoding is ours (parseNeighbor).
func readNeighbors() ([]neighbor, error) {
	raw, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, syscall.AF_INET)
	if err != nil {
		return nil, err
	}
	msgs, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return nil, err
	}
	var out []neighbor
	for _, m := range msgs {
		switch m.Header.Type {
		case syscall.NLMSG_DONE:
			return out, nil
		case syscall.RTM_NEWNEIGH:
			if n, ok := parseNeighbor(m.Data); ok {
				out = append(out, n)
			}
		}
	}
	return out, nil
}
