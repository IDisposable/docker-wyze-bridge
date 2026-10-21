package lanresolve

import (
	"encoding/binary"
	"net"
)

// ARP over Ethernet for IPv4 (RFC 826) is a fixed 28-byte payload. The
// Ethernet header is left to the kernel: the engine uses a SOCK_DGRAM
// packet socket, which takes the destination MAC from the sockaddr and
// prepends the frame header itself.
const (
	arpPacketLen = 28

	arpHTypeEthernet uint16 = 1
	arpPTypeIPv4     uint16 = 0x0800
	arpOpRequest     uint16 = 1
	arpOpReply       uint16 = 2
)

// arpRequest builds a "who has target, tell sender" request. Target
// hardware address is zero, as the RFC prescribes for requests.
func arpRequest(senderHW net.HardwareAddr, senderIP, targetIP net.IP) []byte {
	b := make([]byte, arpPacketLen)
	binary.BigEndian.PutUint16(b[0:], arpHTypeEthernet)
	binary.BigEndian.PutUint16(b[2:], arpPTypeIPv4)
	b[4] = 6 // hardware address length
	b[5] = 4 // protocol address length
	binary.BigEndian.PutUint16(b[6:], arpOpRequest)
	copy(b[8:14], senderHW)
	copy(b[14:18], senderIP.To4())
	// b[18:24] target hardware: zero
	copy(b[24:28], targetIP.To4())
	return b
}

// arpReply is the part of an answer the resolver cares about: which
// hardware address claims which IP.
type arpReply struct {
	SenderHW net.HardwareAddr
	SenderIP net.IP
}

// parseARPReply decodes an ARP reply. Requests, other protocols, and
// truncated frames return ok=false. Gratuitous replies (sender == target)
// decode like any other; the caller matches on sender fields only.
func parseARPReply(b []byte) (arpReply, bool) {
	if len(b) < arpPacketLen {
		return arpReply{}, false
	}
	if binary.BigEndian.Uint16(b[0:]) != arpHTypeEthernet ||
		binary.BigEndian.Uint16(b[2:]) != arpPTypeIPv4 ||
		b[4] != 6 || b[5] != 4 ||
		binary.BigEndian.Uint16(b[6:]) != arpOpReply {
		return arpReply{}, false
	}
	hw := make(net.HardwareAddr, 6)
	copy(hw, b[8:14])
	ip := make(net.IP, 4)
	copy(ip, b[14:18])
	return arpReply{SenderHW: hw, SenderIP: ip}, true
}
