// Package gwell implements the Gwell/IoTVideo P2P protocol for Wyze GW cameras.
//
// Protocol flow (from live log analysis):
//  1. UDP ListFrmRequest -> list servers (port 51701) -> LIST_RESP with P2P servers
//  2. UDP detecReq (68 bytes) -> each P2P server -> detect response
//  3. DTLS certify_req -> best P2P server -> certify_resp (session_id)
//  4. Mars init_info_msg (accessId+token) -> DID<->TID mapping
//  5. subscribe_dev(tid, token) -> IV_ACCESS_SRV_LINK_ONLINE
//  6. CALLING(did) -> STUN/relay -> AVSTREAMCTL_ACCEPT -> H.264 stream
package gwell

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// ErrNoServers is returned when no P2P servers could be discovered or reached.
var ErrNoServers = fmt.Errorf("no P2P servers responded")

// Known list servers from live PCAP capture
var ListServers = []string{
	"34.215.36.59:51701",
	"18.118.90.161:51701",
	"35.85.21.174:51701",
}

// KnownP2PServers are the servers captured from live Android logs.
// Used as fallback when dynamic discovery fails.
var KnownP2PServers = []P2PServer{
	{IP: net.IPv4(3, 13, 212, 24), Port: 28800, ServerID: 49},
	{IP: net.IPv4(52, 201, 137, 206), Port: 28800, ServerID: 59},
	{IP: net.IPv4(35, 81, 136, 54), Port: 8000, ServerID: 38},
	{IP: net.IPv4(54, 208, 16, 245), Port: 443, ServerID: 58},
	{IP: net.IPv4(44, 238, 104, 252), Port: 443, ServerID: 18},
	{IP: net.IPv4(52, 40, 221, 253), Port: 51705, ServerID: 28},
	{IP: net.IPv4(3, 131, 23, 11), Port: 8443, ServerID: 48},
}

// P2PServer represents a discovered P2P relay/STUN server.
// 36-byte binary format (decoded from p2pSave/3.p2p):
//
//	Offset  Size  Field
//	0       4     IPv4 address (network byte order)
//	4       16    IPv6 address (zeros if IPv4-only)
//	20      2     ip_version (1=IPv4)
//	22      2     server_id (little-endian)
//	24      2     port (big-endian)
//	26      2     port2 (big-endian, typically same as port)
//	28      4     timestamp/level info
//	32      4     reserved (zeros)
type P2PServer struct {
	IP       net.IP
	IPv6     net.IP
	Port     uint16
	ServerID uint16
	IPVer    uint16
}

const p2pServerEntrySize = 36

func (s P2PServer) Addr() string {
	return fmt.Sprintf("%s:%d", s.IP, s.Port)
}

func (s P2PServer) String() string {
	return fmt.Sprintf("%s:%d (id=%d)", s.IP, s.Port, s.ServerID)
}

// ParseP2PServerEntry decodes a 36-byte server entry from the binary cache.
func ParseP2PServerEntry(data []byte) P2PServer {
	return P2PServer{
		IP:       net.IPv4(data[0], data[1], data[2], data[3]),
		IPv6:     net.IP(data[4:20]),
		IPVer:    binary.LittleEndian.Uint16(data[20:22]),
		ServerID: binary.LittleEndian.Uint16(data[22:24]),
		Port:     binary.BigEndian.Uint16(data[24:26]),
	}
}

// ParseP2PServerList decodes the full p2pSave cache format.
// Header: 4-byte LE count, then count * 36-byte entries.
func ParseP2PServerList(data []byte) []P2PServer {
	if len(data) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	var servers []P2PServer
	for i := 0; i < count; i++ {
		off := 4 + i*p2pServerEntrySize
		if off+p2pServerEntrySize > len(data) {
			break
		}
		srv := ParseP2PServerEntry(data[off : off+p2pServerEntrySize])
		if srv.IP.Equal(net.IPv4zero) {
			continue
		}
		servers = append(servers, srv)
	}
	return servers
}

// DiscoverServers tries UDP ListFrmRequest to get the P2P server list.
// HTTP discovery is intentionally skipped — the SDK doesn't use it and it
// adds ~60s of timeouts against unresponsive list servers.
func DiscoverServers() ([]P2PServer, error) {
	log.Printf("[discovery] Starting server discovery (UDP only)")

	// Try UDP ListFrmRequest with short timeout
	for _, addr := range ListServers {
		servers, err := sendListRequest(addr)
		if err != nil {
			log.Printf("[discovery] UDP to %s failed: %v", addr, err)
			continue
		}
		if len(servers) > 0 {
			return servers, nil
		}
	}
	return nil, fmt.Errorf("no P2P servers discovered from any list server")
}

// DiscoverServersOrFallback tries dynamic discovery, falls back to hardcoded servers.
func DiscoverServersOrFallback() []P2PServer {
	servers, err := DiscoverServers()
	if err != nil || len(servers) == 0 {
		log.Printf("[discovery] Dynamic discovery failed, using %d hardcoded servers", len(KnownP2PServers))
		return KnownP2PServers
	}
	return servers
}

// httpListRequest tries the HTTP endpoint to get the server list.
// Path from binary: /iotvideo/service/ListService/GetServiceList
func httpListRequest(host string) ([]P2PServer, error) {
	// Try common ports
	for _, port := range []int{80, 443, 51701, 8080} {
		scheme := "http"
		if port == 443 {
			scheme = "https"
		}
		url := fmt.Sprintf("%s://%s:%d/iotvideo/service/ListService/GetServiceList", scheme, host, port)

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("[discovery] HTTP GET %s: %v", url, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		log.Printf("[discovery] HTTP %s -> %d, body (%d bytes): %s", url, resp.StatusCode, len(body), string(body[:min(len(body), 200)]))

		if resp.StatusCode == 200 && len(body) > 0 {
			// Try to parse as server list
			servers := tryParseListResp(body)
			if len(servers) > 0 {
				return servers, nil
			}
		}
	}
	return nil, fmt.Errorf("HTTP discovery failed for %s", host)
}

// sendListRequest sends a UDP probe to a list server and logs the raw response.
// The exact ListFrmRequest frame format is still being reverse-engineered.
// For now, we send a minimal probe and dump whatever comes back.
func sendListRequest(addr string) ([]P2PServer, error) {
	conn, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	frame := buildListFrmRequest()

	conn.SetDeadline(time.Now().Add(2 * time.Second))

	_, err = conn.Write(frame)
	if err != nil {
		return nil, fmt.Errorf("send to %s: %w", addr, err)
	}

	log.Printf("[discovery] Sent %d byte ListFrmRequest to %s", len(frame), addr)

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read from %s: %w", addr, err)
	}

	log.Printf("[discovery] Received %d byte response from %s", n, addr)
	// Dump first 128 bytes hex for analysis
	dumpLen := n
	if dumpLen > 128 {
		dumpLen = 128
	}
	for i := 0; i < dumpLen; i += 16 {
		end := i + 16
		if end > dumpLen {
			end = dumpLen
		}
		log.Printf("[discovery]   %04x: %x", i, buf[i:end])
	}

	// Try to parse the response as a server list
	// The response may have a frame header before the server list data
	servers := tryParseListResp(buf[:n])
	return servers, nil
}

// buildListFrmRequest constructs a ListFrmRequest UDP frame.
// Total size: 40 bytes. Proto=0x7F, sub=0x15, len=40.
// Frame layout verified from PCAP capture.
func buildListFrmRequest() []byte {
	return BuildListFrmRequest()
}

// tryParseListResp attempts to find P2P server entries in the response data.
// It searches for the 4-byte-count + 36-byte-entry pattern.
func tryParseListResp(data []byte) []P2PServer {
	// Try parsing from different offsets (the response may have a header)
	for offset := 0; offset < len(data)-40; offset += 4 {
		count := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if count < 1 || count > 20 {
			continue
		}

		expectedLen := 4 + count*p2pServerEntrySize
		if offset+expectedLen > len(data) {
			continue
		}

		// Try to parse server entries
		servers := ParseP2PServerList(data[offset : offset+expectedLen])
		if len(servers) > 0 {
			// Validate: check if any IPs look like real public IPs
			valid := false
			for _, s := range servers {
				ip4 := s.IP.To4()
				if ip4 != nil && ip4[0] != 0 && ip4[0] != 127 && ip4[0] != 10 {
					valid = true
					break
				}
			}
			if valid {
				log.Printf("[discovery] Found %d servers at response offset %d", len(servers), offset)
				for i, s := range servers {
					log.Printf("[discovery]   Server %d: %s", i, s)
				}
				return servers
			}
		}
	}

	log.Printf("[discovery] Could not parse server list from %d byte response", len(data))
	return nil
}

// DetectBestServer sends 68-byte detect requests to all P2P servers
// and returns the one with the lowest latency.
func DetectBestServer(servers []P2PServer) (*P2PServer, error) {
	if len(servers) == 0 {
		return nil, fmt.Errorf("no servers to detect")
	}

	type result struct {
		server  *P2PServer
		latency time.Duration
		err     error
	}

	results := make(chan result, len(servers))

	for i := range servers {
		go func(srv *P2PServer) {
			start := time.Now()
			err := sendDetectReq(srv)
			results <- result{
				server:  srv,
				latency: time.Since(start),
				err:     err,
			}
		}(&servers[i])
	}

	var best *P2PServer
	var bestLatency time.Duration

	for i := 0; i < len(servers); i++ {
		r := <-results
		if r.err != nil {
			log.Printf("[detect] %s failed: %v", r.server, r.err)
			continue
		}
		log.Printf("[detect] %s responded in %v", r.server, r.latency)
		if best == nil || r.latency < bestLatency {
			best = r.server
			bestLatency = r.latency
		}
	}

	if best == nil {
		return nil, fmt.Errorf("all servers failed detection")
	}
	return best, nil
}

// sendDetectReq sends a 68-byte encrypted DetectRequest2 to a P2P server.
// All outbound frames must be encrypted (PCAP confirms plaintext is ignored).
func sendDetectReq(server *P2PServer) error {
	conn, err := net.DialTimeout("udp", server.Addr(), 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	frame := BuildDetectReq2()
	// Apply full encryption pipeline (checksum already set by BuildDetectReq2,
	// but EncryptFrameFull recomputes it before encrypting)
	pwdKey := NewPasswordKey()
	EncryptFrameFull(frame, pwdKey)

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Write(frame)
	if err != nil {
		return err
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}

	log.Printf("[detect] Got %d byte response from %s: %x", n, server, buf[:min(n, 64)])

	if n >= 24 {
		if VerifyChkval(buf[:n]) {
			log.Printf("[detect] Response checksum valid (plaintext)")
		} else {
			// Try full decryption
			if DecryptFrameFull(buf[:n], pwdKey) {
				log.Printf("[detect] Response checksum valid (after decrypt)")
			} else {
				log.Printf("[detect] Response checksum invalid")
			}
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
