package gwell

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// SessionState tracks protocol phase.
type SessionState int

const (
	StateNew SessionState = iota
	StateDetected
	StateCertified
	StateSubscribed
	StateCalling
	StateTransfer
	StateStreaming
	StateClosed
)

func (s SessionState) String() string {
	names := [...]string{"new", "detected", "certified", "subscribed", "calling", "transfer", "streaming", "closed"}
	if int(s) < len(names) {
		return names[s]
	}
	return "unknown"
}

// Frame represents a parsed GUTES frame.
type Frame struct {
	Proto    byte
	SubType  byte
	FrameLen uint16
	Word1    uint32
	Word2    uint32
	Sqnum    uint32
	Chkval   uint32
	Flags    uint32
	Payload  []byte
	Raw      []byte
}

// ParseFrame parses raw bytes into a Frame struct.
func ParseFrame(data []byte) (*Frame, error) {
	if len(data) < FrameHeaderSize {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}
	f := &Frame{
		Proto:    data[0],
		SubType:  data[1],
		FrameLen: binary.LittleEndian.Uint16(data[2:4]),
		Word1:    binary.LittleEndian.Uint32(data[4:8]),
		Word2:    binary.LittleEndian.Uint32(data[8:12]),
		Sqnum:    binary.LittleEndian.Uint32(data[12:16]),
		Chkval:   binary.LittleEndian.Uint32(data[16:20]),
		Flags:    binary.LittleEndian.Uint32(data[20:24]),
	}
	if len(data) > FrameHeaderSize {
		f.Payload = data[FrameHeaderSize:]
	}
	raw := make([]byte, len(data))
	copy(raw, data)
	f.Raw = raw
	return f, nil
}

// SubTypeName returns a human-readable name for the frame sub-type.
func (f *Frame) SubTypeName() string {
	switch f.SubType {
	case SubTypeDetectReq2:
		return "DetectReq2"
	case SubTypeDetectResp:
		return "DetectResp"
	case SubTypeSessionInit:
		return "SessionInit"
	case SubTypeSessionResp:
		return "SessionResp"
	case SubTypeHeartbeat:
		return "Heartbeat"
	case SubTypeInitInfoMsg:
		return "InitInfoMsg"
	case SubTypeSubscribe:
		return "Subscribe"
	case SubTypeCalling:
		return "Calling"
	case 0xA3:
		return "MTP_RES"
	case 0xB9:
		return "Passthrough"
	case 0xCA:
		return "PortStatReq"
	case 0xCB:
		return "PortStatResp"
	default:
		return fmt.Sprintf("0x%02X", f.SubType)
	}
}

// SessionConfig configures a new P2P camera session.
type SessionConfig struct {
	Token       *AccessToken
	ServerAddr  string       // P2P server address (auto-discovered if empty)
	CameraLanIP string       // camera's LAN IP for direct connection
	DeviceName  string       // target device name filter
	H264Writer  io.Writer    // receives decoded H.264 data (optional)
	Devices     []DeviceInfo // pre-populated device list (skips initInfo if set)
}

// Session manages the full lifecycle of a P2P camera connection.
type Session struct {
	cfg    SessionConfig
	pwdKey *RC5Key
	prefix string

	// P2P server connection
	conn          net.Conn
	serverAddr    string
	serverUDPAddr *net.UDPAddr

	// Session state (from CertifyResp)
	certResult       *CertifyResult
	routingSessionID uint64
	sqnum            uint32

	// Device info
	devices   []DeviceInfo
	targetDev DeviceInfo

	// MTP transport
	linkID           uint32
	mtpRC5Ctx        *RC5Key
	pc               *net.UDPConn
	lanMTPAddrs      []*net.UDPAddr
	relayAddrs       []RelayAddr
	discoveredRelays sync.Map
	udpRelayTargets  []UDPRelayTarget
	tcpRelay         net.Conn

	// KCP state
	dataKCP  *KCPConn
	ctrlKCP  *KCPConn
	convData uint32
	convCtrl uint32

	// Stream counters
	streamPkts      int
	streamDataBytes int
	lastDataFrom    string
	bestLanAddr     *net.UDPAddr // best LAN address for KCP output (camera's actual IP:port)
	meterRound      uint32       // per-session meter probe round counter

	// Diagnostics
	rawUDPPkts      int64     // total raw UDP packets received during streaming
	lastMeterRecv   time.Time // last time we received any meter frame (REQ or ACK)
	meterReqCount   int       // incoming meter REQUESTs from camera (since last log)
	meterAckCount   int       // incoming meter ACKs from camera (since last log)

	// Lifecycle
	state  SessionState
	closed int32
}

// NewSession creates a new P2P session for a camera.
func NewSession(cfg SessionConfig) *Session {
	name := cfg.DeviceName
	if name == "" {
		name = "cam"
	}
	return &Session{
		cfg:    cfg,
		pwdKey: NewPasswordKey(),
		prefix: fmt.Sprintf("[%s]", name),
		sqnum:  1,
	}
}

// DiscoveryResult holds the output from device discovery.
type DiscoveryResult struct {
	Devices    []DeviceInfo
	ServerAddr string // working P2P server address (pass to SessionConfig.ServerAddr)
}

// DiscoverDevices connects to a P2P server, certifies, and returns the device list
// along with the working server address. Pass ServerAddr and Devices to SessionConfig
// for subsequent sessions (avoids re-discovery and P2P server rate limiting).
func DiscoverDevices(token *AccessToken) (*DiscoveryResult, error) {
	s := NewSession(SessionConfig{
		Token:      token,
		DeviceName: "discovery",
	})
	defer s.Close()

	if err := s.connect(); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := s.certify(); err != nil {
		return nil, fmt.Errorf("certify: %w", err)
	}
	if err := s.initInfo(); err != nil {
		return nil, fmt.Errorf("initInfo: %w", err)
	}
	return &DiscoveryResult{
		Devices:    s.devices,
		ServerAddr: s.serverAddr,
	}, nil
}

// SetStreamCallback is kept for compatibility but H264Writer is preferred.
func (s *Session) SetStreamCallback(cb func(data []byte, mediaType string)) {}

// Close terminates the session.
func (s *Session) Close() error {
	if !atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		return nil
	}
	s.state = StateClosed
	if s.pc != nil {
		s.pc.Close()
	}
	if s.conn != nil {
		s.conn.Close()
	}
	if s.tcpRelay != nil {
		s.tcpRelay.Close()
	}
	return nil
}

func (s *Session) isClosed() bool {
	return atomic.LoadInt32(&s.closed) != 0
}

func (s *Session) nextSqnum() uint32 {
	v := s.sqnum
	s.sqnum++
	return v
}

// Run executes the full P2P session lifecycle. Blocks until session ends or error.
func (s *Session) Run(deviceID string) error {
	defer s.Close()

	// Phase 1: Connect to P2P server
	if err := s.connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	// Phase 2: CertifyReq → CertifyResp
	if err := s.certify(); err != nil {
		return fmt.Errorf("certify: %w", err)
	}

	// Phase 3: InitInfoMsg → device list + routing session ID.
	// Pre-populate devices so initInfo won't fail if P2P server skips 0xA7
	// (happens when multiple sessions from same account connect).
	if len(s.cfg.Devices) > 0 {
		s.devices = s.cfg.Devices
		log.Printf("%s pre-populated %d devices", s.prefix, len(s.devices))
	}
	if err := s.initInfo(); err != nil {
		return fmt.Errorf("initInfo: %w", err)
	}

	// Phase 4: Network detect probe
	s.networkDetect()

	// Phase 5: Subscribe
	s.subscribe()

	// Phase 6: CALLING
	if err := s.calling(); err != nil {
		return fmt.Errorf("calling: %w", err)
	}

	// Phase 7: MTP transport + KCP stream
	return s.streamLoop()
}

// connect discovers and connects to a P2P server.
func (s *Session) connect() error {
	var conn net.Conn
	var serverAddr string
	var err error

	if s.cfg.ServerAddr != "" {
		serverAddr = s.cfg.ServerAddr
		conn, err = net.DialTimeout("udp", serverAddr, 5*time.Second)
		if err != nil {
			return fmt.Errorf("connect to %s: %w", serverAddr, err)
		}
	} else {
		servers := DiscoverServersOrFallback()
		log.Printf("%s discovered %d P2P servers", s.prefix, len(servers))
		for _, srv := range servers {
			addr := srv.Addr()
			c, dialErr := net.DialTimeout("udp", addr, 2*time.Second)
			if dialErr != nil {
				continue
			}
			req := BuildDetectReq2()
			EncryptFrameFull(req, s.pwdKey)
			c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			c.Write(req)
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 1024)
			n, readErr := c.Read(buf)
			if readErr == nil && n >= FrameHeaderSize {
				conn = c
				serverAddr = addr
				break
			}
			c.Close()
		}
		if conn == nil {
			return ErrNoServers
		}
	}

	s.conn = conn
	s.serverAddr = serverAddr
	log.Printf("%s connected to P2P server %s", s.prefix, serverAddr)
	return nil
}

// certify performs CertifyReq → CertifyResp handshake.
func (s *Session) certify() error {
	token := s.cfg.Token
	buf := make([]byte, 8192)

	certifyReq, randomKey := BuildCertifyReq(token, s.nextSqnum())
	log.Printf("%s sending CertifyReq (%d bytes)", s.prefix, len(certifyReq))

	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write(certifyReq)

	for attempt := 0; attempt < 3; attempt++ {
		s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := s.conn.Read(buf)
		if err != nil {
			if attempt == 0 {
				// Retry without outer mode-1 encryption
				certifyReq2, randomKey2 := BuildCertifyReqRaw(token, s.nextSqnum())
				randomKey = randomKey2
				s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				s.conn.Write(certifyReq2)
				continue
			}
			return fmt.Errorf("no CertifyResp after %d attempts", attempt+1)
		}

		resp := make([]byte, n)
		copy(resp, buf[:n])
		if DecryptFrameFull(resp, s.pwdKey) {
			if resp[1] == SubTypeSessionInit || resp[1] == SubTypeSessionResp {
				resp2 := make([]byte, n)
				copy(resp2, buf[:n])
				result, parseErr := ParseCertifyResp(resp2, randomKey, s.pwdKey)
				if parseErr == nil {
					s.certResult = result
					s.state = StateCertified
					log.Printf("%s CertifyResp OK: sessionID=0x%016X", s.prefix, result.SessionID)
					return nil
				}
			}
		}

		// Try brute-force parse
		copy(resp, buf[:n])
		DecryptID(resp, s.pwdKey)
		DecryptFrame(resp)
		if (resp[1] == SubTypeSessionInit || resp[1] == SubTypeSessionResp) && n >= 36 {
			errCode := binary.LittleEndian.Uint16(resp[26:28])
			sessionID := binary.LittleEndian.Uint64(resp[28:36])
			if errCode == 0 && sessionID != 0 {
				s.certResult = &CertifyResult{
					SessionID:  sessionID,
					SessionKey: NewRC5Key(randomKey[:]),
					RandomKey:  randomKey,
				}
				s.state = StateCertified
				log.Printf("%s CertifyResp OK (fallback): sessionID=0x%016X", s.prefix, sessionID)
				return nil
			}
		}
	}
	return fmt.Errorf("CertifyResp: no valid response")
}

// initInfo sends InitInfoMsg and parses device list.
func (s *Session) initInfo() error {
	token := s.cfg.Token
	result := s.certResult
	buf := make([]byte, 8192)

	initMsg := BuildInitInfoMsg(token, result.SessionID, s.nextSqnum(), result.SessionKey, s.pwdKey)
	log.Printf("%s sending InitInfoMsg (%d bytes)", s.prefix, len(initMsg))

	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write(initMsg)

	for i := 0; i < 10; i++ {
		s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := s.conn.Read(buf)
		if err != nil {
			log.Printf("%s initInfo resp[%d]: read error: %v", s.prefix, i, err)
			break
		}

		log.Printf("%s initInfo resp[%d]: %d bytes, raw[0:2]=0x%02X%02X", s.prefix, i, n, buf[0], buf[1])
		decrypted, mode := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
		if decrypted == nil {
			log.Printf("%s initInfo resp[%d]: decrypt FAILED", s.prefix, i)
			continue
		}
		log.Printf("%s initInfo resp[%d]: decrypted sub=0x%02X proto=0x%02X (%s)", s.prefix, i, decrypted[1], decrypted[0], mode)

		// Parse routing session ID from 0x0D
		if decrypted[1] == SubTypeSessionResp && n >= 36 && s.routingSessionID == 0 {
			s.routingSessionID = binary.LittleEndian.Uint64(decrypted[28:36])
			log.Printf("%s routing sessionID=0x%016X", s.prefix, s.routingSessionID)
		}

		// Parse device list from 0xA7
		if decrypted[1] == 0xA7 && n > 24 {
			devs := ParseInitInfoResp(decrypted[24:n])
			if len(devs) > 0 {
				s.devices = devs
				log.Printf("%s found %d devices", s.prefix, len(devs))
				for _, d := range devs {
					log.Printf("%s   device: %q TID=0x%016X", s.prefix, d.Name, d.TID)
				}
			} else {
				log.Printf("%s initInfo resp[%d]: 0xA7 but ParseInitInfoResp returned 0 devices (payload %d bytes)", s.prefix, i, n-24)
			}
		}

		// Break early if we have both routing ID and devices
		if s.routingSessionID != 0 && len(s.devices) > 0 {
			break
		}
	}

	if len(s.devices) == 0 {
		return fmt.Errorf("no devices from InitInfoResp")
	}

	// Select target device
	s.targetDev = s.devices[0]
	if s.cfg.DeviceName != "" {
		for _, d := range s.devices {
			if d.Name == s.cfg.DeviceName {
				s.targetDev = d
				break
			}
		}
	}
	log.Printf("%s target device: %q TID=0x%016X", s.prefix, s.targetDev.Name, s.targetDev.TID)
	return nil
}

// networkDetect sends Network Detect Probe (0xB9) if LAN IP is set.
func (s *Session) networkDetect() {
	if s.cfg.CameraLanIP == "" {
		return
	}
	token := s.cfg.Token
	result := s.certResult
	buf := make([]byte, 8192)

	ourLanIP := GetOutboundIP(s.cfg.CameraLanIP)
	if ourLanIP == nil {
		ourLanIP = net.ParseIP(s.cfg.CameraLanIP)
	}

	detectProbe := BuildNetworkDetectProbe(token, s.routingSessionID, s.nextSqnum(),
		s.targetDev.TID, s.pwdKey, ourLanIP.String(), 5, 3000)
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write(detectProbe)
	log.Printf("%s sent network detect probe (LAN IP=%s)", s.prefix, ourLanIP)

	// Wait 5 seconds for camera to process
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := s.conn.Read(buf)
		if err != nil {
			continue
		}
		decrypted, mode := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
		if decrypted != nil {
			log.Printf("%s network detect resp: sub=0x%02X (%s)", s.prefix, decrypted[1], mode)
		}
	}
}

// subscribe sends DevID and token-based subscribe messages.
func (s *Session) subscribe() {
	token := s.cfg.Token
	result := s.certResult
	buf := make([]byte, 8192)
	useRouting := s.routingSessionID != 0

	// DevID subscribe
	subMsg := BuildSubscribeDevID(token, s.routingSessionID, s.nextSqnum(),
		token.AccessID, useRouting, result.SessionKey, s.pwdKey)
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write(subMsg)
	log.Printf("%s sent subscribe(devid)", s.prefix)

	// Token subscribe
	if len(token.ExtraTokenData) > 0 {
		subTokenMsg := BuildSubscribeTokens(token, s.routingSessionID, s.nextSqnum(),
			token.ExtraTokenData, useRouting, result.SessionKey, s.pwdKey)
		s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		s.conn.Write(subTokenMsg)
		log.Printf("%s sent subscribe(token)", s.prefix)
	}

	// Read responses
	for i := 0; i < 5; i++ {
		s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := s.conn.Read(buf)
		if err != nil {
			break
		}
		decrypted, _ := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
		if decrypted != nil {
			log.Printf("%s subscribe resp[%d]: sub=0x%02X", s.prefix, i, decrypted[1])
		}
	}
	s.state = StateSubscribed
}

// calling sends CALLING message and collects relay addresses from MTP_RES_RESPONSE.
func (s *Session) calling() error {
	token := s.cfg.Token
	result := s.certResult
	buf := make([]byte, 8192)

	s.linkID = mathrand.Uint32()
	if s.linkID == 0 {
		s.linkID = 1
	}

	// Generate MTP RC5 key
	mtpKey := make([]byte, 8)
	rand.Read(mtpKey)
	s.mtpRC5Ctx = NewRC5Key(mtpKey)

	// Determine our LAN address for CALLING frame
	var ourLanIP net.IP
	var ourLanPort uint16
	if s.cfg.CameraLanIP != "" {
		ourLanIP = GetOutboundIP(s.cfg.CameraLanIP)
		localAddr := s.conn.LocalAddr().(*net.UDPAddr)
		ourLanPort = uint16(localAddr.Port)
	}

	callingMsg := BuildCallingMsg(token, s.routingSessionID, s.nextSqnum(),
		s.linkID, s.targetDev.TID, result.SessionKey, s.pwdKey,
		ourLanIP, ourLanPort, mtpKey, false)
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write(callingMsg)
	log.Printf("%s sent CALLING linkID=0x%08X target=0x%016X", s.prefix, s.linkID, s.targetDev.TID)
	s.state = StateCalling

	// Collect responses: CALLING ACK + MTP_RES_RESPONSE
	var callingRelayIP net.IP
	var callingRelayPort uint16
	var peerOuterPort, peerSessionPort, peerLanPort uint16
	var peerOuterIP net.IP

	for i := 0; i < 20; i++ {
		s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := s.conn.Read(buf)
		if err != nil {
			break
		}

		if buf[0] == 0xC0 {
			continue // MTP frame during CALLING phase
		}

		decrypted, _ := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
		if decrypted == nil {
			continue
		}

		sub := decrypted[1]

		// CALLING ACK
		if sub == SubTypeCalling && n >= 38 {
			callingRelayIP = net.IPv4(decrypted[32], decrypted[33], decrypted[34], decrypted[35])
			callingRelayPort = binary.LittleEndian.Uint16(decrypted[36:38])
			log.Printf("%s CALLING ACK: peer=%s:%d", s.prefix, callingRelayIP, callingRelayPort)
		}

		// MTP_RES_RESPONSE
		if sub == 0xA3 && n >= 0x68 {
			peerOuterPort = binary.LittleEndian.Uint16(decrypted[0x58:0x5A])
			peerOuterIP = net.IPv4(decrypted[0x60], decrypted[0x61], decrypted[0x62], decrypted[0x63])
			peerSessionPort = binary.LittleEndian.Uint16(decrypted[0x5E:0x60])
			if n >= 0x68 {
				peerLanPort = binary.LittleEndian.Uint16(decrypted[0x5A:0x5C])
			}
			log.Printf("%s MTP_RES: outer=%s:%d session=%d lan=%d",
				s.prefix, peerOuterIP, peerOuterPort, peerSessionPort, peerLanPort)

			// Parse relay nodes
			if n >= 0x7B {
				relayV4Count := int(decrypted[0x78])
				if relayV4Count > 32 {
					relayV4Count = 32
				}
				relayOff := 0x7A
				for ri := 0; ri < relayV4Count && relayOff+16 <= n; ri++ {
					rFlags := binary.LittleEndian.Uint16(decrypted[relayOff+8 : relayOff+10])
					rPort := binary.LittleEndian.Uint16(decrypted[relayOff+10 : relayOff+12])
					rIP := net.IPv4(decrypted[relayOff+12], decrypted[relayOff+13], decrypted[relayOff+14], decrypted[relayOff+15])
					isTCP := (rFlags>>2)&1 == 1
					relaySessID := binary.LittleEndian.Uint64(decrypted[relayOff : relayOff+8])
					s.relayAddrs = append(s.relayAddrs, RelayAddr{IP: rIP, Port: rPort, TCP: isTCP, SessionID: relaySessID})
					relayOff += 16
				}
				log.Printf("%s got %d relay addresses", s.prefix, len(s.relayAddrs))
			}
			// Break immediately — relay servers expire quickly
			break
		}
	}

	if callingRelayIP == nil && s.cfg.CameraLanIP == "" && len(s.relayAddrs) == 0 {
		return fmt.Errorf("no CALLING response (no relay, no LAN)")
	}

	// --- Phase 5: Setup UDP transport ---
	// Close connected socket, reopen as unconnected for multi-target
	localAddr := s.conn.LocalAddr().(*net.UDPAddr)
	localPort := localAddr.Port
	s.conn.Close()

	pc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: localPort})
	if err != nil {
		pc, err = net.ListenUDP("udp4", nil)
		if err != nil {
			return fmt.Errorf("ListenUDP: %w", err)
		}
	}
	s.pc = pc

	s.serverUDPAddr, _ = net.ResolveUDPAddr("udp", s.serverAddr)

	// Build camera address list
	if s.cfg.CameraLanIP != "" {
		lanIP := net.ParseIP(s.cfg.CameraLanIP)
		if lanIP != nil {
			seen := make(map[uint16]bool)
			addPort := func(p uint16) {
				if p != 0 && !seen[p] {
					seen[p] = true
					addr := &net.UDPAddr{IP: lanIP, Port: int(p)}
					s.lanMTPAddrs = append(s.lanMTPAddrs, addr)
				}
			}
			addPort(peerSessionPort)
			addPort(peerLanPort)
			addPort(callingRelayPort)
			addPort(peerOuterPort)
			addPort(6789)
			addPort(32761)
			addPort(32100)
		}
	}

	// Send MTP_RES_REQUEST
	mtpResReq := BuildMTPResRequest(token, s.linkID, s.targetDev.TID,
		s.routingSessionID, result.SessionKey, s.pwdKey)
	pc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	pc.WriteToUDP(mtpResReq, s.serverUDPAddr)
	pc.SetWriteDeadline(time.Time{})

	// Start relay activation goroutines
	tcpRelayChan := make(chan net.Conn, 1)
	s.startRelayActivation(tcpRelayChan)

	// Probe loop: PortStatReq + detect + wait for CREATE_KCP
	s.probeAndWait(callingRelayIP, callingRelayPort, peerOuterPort, peerOuterIP, tcpRelayChan)

	// Setup transport priority
	s.setupTransport(callingRelayIP, callingRelayPort, peerOuterIP, peerOuterPort, tcpRelayChan)

	s.state = StateTransfer
	return nil
}

// startRelayActivation launches background goroutines for relay port activation.
func (s *Session) startRelayActivation(tcpRelayChan chan net.Conn) {
	token := s.cfg.Token
	if len(s.relayAddrs) == 0 {
		// P2P server TCP fallback
		go func() {
			time.Sleep(500 * time.Millisecond)
			dialer := net.Dialer{Timeout: 5 * time.Second}
			c, err := dialer.Dial("tcp", s.serverAddr)
			if err != nil {
				tcpRelayChan <- nil
				return
			}
			regFrame := BuildTCPRelayRegister(s.linkID, token.AccessID, s.targetDev.TID)
			c.SetWriteDeadline(time.Now().Add(3 * time.Second))
			c.Write(regFrame)
			c.SetWriteDeadline(time.Time{})
			log.Printf("%s P2P TCP fallback connected", s.prefix)
			tcpRelayChan <- c
		}()
		return
	}

	// Session socket activation
	go func() {
		var sessionSocketAddrs []*net.UDPAddr
		// Add camera addresses
		if s.cfg.CameraLanIP != "" {
			for _, addr := range s.lanMTPAddrs {
				sessionSocketAddrs = append(sessionSocketAddrs, addr)
			}
		}
		sessionSocketAddrs = append(sessionSocketAddrs, s.serverUDPAddr)
		for _, ra := range s.relayAddrs {
			raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ra.IP, ra.Port))
			if err == nil {
				sessionSocketAddrs = append(sessionSocketAddrs, raddr)
			}
		}
		var relayPortList []uint16
		for _, ra := range s.relayAddrs {
			relayPortList = append(relayPortList, ra.Port)
			if len(relayPortList) >= 4 {
				break
			}
		}
		for round := 0; round < 10; round++ {
			subType := byte(1)
			if round == 2 {
				subType = 2
			}
			ssFrame := BuildSessionSocket(token, s.routingSessionID, s.sqnum,
				s.linkID, s.targetDev.TID, subType, relayPortList, s.pwdKey)
			for _, addr := range sessionSocketAddrs {
				s.pc.WriteToUDP(ssFrame, addr)
			}
			time.Sleep(400 * time.Millisecond)
		}
	}()

	// UDP pre-registration
	go func() {
		regFrame := BuildTCPRelayRegister(s.linkID, token.AccessID, s.targetDev.TID)
		seen := make(map[netip.AddrPort]bool)
		for _, ra := range s.relayAddrs {
			addr, err := netip.ParseAddr(ra.IP.String())
			if err != nil {
				continue
			}
			ap := netip.AddrPortFrom(addr, ra.Port)
			if seen[ap] {
				continue
			}
			seen[ap] = true
			raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ra.IP, ra.Port))
			if raddr != nil {
				s.pc.WriteToUDP(regFrame, raddr)
			}
		}
	}()

	// TCP relay
	go func() {
		time.Sleep(200 * time.Millisecond)
		seen := make(map[netip.AddrPort]bool)
		var uniqueAddrs []string
		for _, ra := range s.relayAddrs {
			if !ra.TCP {
				continue
			}
			addr, err := netip.ParseAddr(ra.IP.String())
			if err != nil {
				continue
			}
			ap := netip.AddrPortFrom(addr, ra.Port)
			if seen[ap] {
				continue
			}
			seen[ap] = true
			uniqueAddrs = append(uniqueAddrs, fmt.Sprintf("%s:%d", ra.IP, ra.Port))
		}
		// Add P2P server as fallback
		if p2pAddr, err := netip.ParseAddrPort(s.serverAddr); err == nil {
			if !seen[p2pAddr] {
				uniqueAddrs = append(uniqueAddrs, s.serverAddr)
			}
		}

		var firstConn net.Conn
		for round := 0; round < 30 && firstConn == nil; round++ {
			if round > 0 {
				time.Sleep(1 * time.Second)
			}
			winner := make(chan net.Conn, len(uniqueAddrs))
			for _, tcpAddr := range uniqueAddrs {
				go func(addr string) {
					dialer := net.Dialer{Timeout: 5 * time.Second}
					c, err := dialer.Dial("tcp", addr)
					if err != nil {
						winner <- nil
						return
					}
					regFrame := BuildTCPRelayRegister(s.linkID, token.AccessID, s.targetDev.TID)
					c.SetWriteDeadline(time.Now().Add(3 * time.Second))
					c.Write(regFrame)
					c.SetWriteDeadline(time.Time{})
					winner <- c
				}(tcpAddr)
			}
			for i := 0; i < len(uniqueAddrs); i++ {
				c := <-winner
				if c != nil {
					if firstConn == nil {
						firstConn = c
					} else {
						c.Close()
					}
				}
			}
		}
		tcpRelayChan <- firstConn
	}()
}

// probeAndWait sends PortStatReq probes and waits for CREATE_KCP from camera.
func (s *Session) probeAndWait(callingRelayIP net.IP, callingRelayPort uint16,
	peerOuterPort uint16, peerOuterIP net.IP, tcpRelayChan chan net.Conn) {

	token := s.cfg.Token
	result := s.certResult
	buf := make([]byte, 8192)
	pc := s.pc

	portStatReq := BuildPortStatReq(token, s.routingSessionID, s.nextSqnum(),
		s.linkID, s.targetDev.TID, s.pwdKey)
	detectReq := BuildDetectReq2()
	EncryptFrameFull(detectReq, s.pwdKey)

	// Send initial probes
	for _, addr := range s.lanMTPAddrs {
		pc.WriteToUDP(portStatReq, addr)
		if s.cfg.CameraLanIP != "" && addr.IP.Equal(net.ParseIP(s.cfg.CameraLanIP)) {
			pc.WriteToUDP(detectReq, addr)
		}
	}
	pc.WriteToUDP(portStatReq, s.serverUDPAddr)

	createKCPReceived := false
	var firstCreateKCPTime time.Time
	kcpSessionSent := false

	for i := 0; i < 30; i++ {
		if createKCPReceived && time.Since(firstCreateKCPTime) >= 3*time.Second {
			break
		}

		pc.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		n, fromAddr, err := pc.ReadFromUDP(buf)
		if err != nil {
			if i%3 == 2 {
				portStatReq = BuildPortStatReq(token, s.routingSessionID, s.nextSqnum(),
					s.linkID, s.targetDev.TID, s.pwdKey)
				for _, addr := range s.lanMTPAddrs {
					pc.WriteToUDP(portStatReq, addr)
				}
				pc.WriteToUDP(portStatReq, s.serverUDPAddr)
				hb := BuildHeartbeat(token, s.routingSessionID, s.nextSqnum(), result.SessionKey, s.pwdKey)
				pc.WriteToUDP(hb, s.serverUDPAddr)
			}
			if createKCPReceived && kcpSessionSent {
				earlyKCP := BuildCreateKCPSessionMsg(s.linkID, token.AccessID, s.targetDev.TID)
				earlyFrame := BuildMTPFrame(earlyKCP, true)
				for _, addr := range s.lanMTPAddrs {
					pc.WriteToUDP(earlyFrame, addr)
				}
			}
			continue
		}

		isFromServer := fromAddr.IP.Equal(s.serverUDPAddr.IP) && fromAddr.Port == s.serverUDPAddr.Port

		if buf[0] == 0xC0 {
			// Track MTP source addresses
			if !isFromServer && fromAddr != nil {
				mtpFlags := buf[1]
				if (mtpFlags>>5)&3 != 0 {
					addrKey := fromAddr.String()
					newAddr := &net.UDPAddr{IP: append(net.IP(nil), fromAddr.IP...), Port: fromAddr.Port}
					s.discoveredRelays.LoadOrStore(addrKey, newAddr)
				} else {
					s.addLanMTPAddr(fromAddr)
				}
			}

			// Check for meter req (CREATE_KCP)
			probePayOff := MTPPayloadOffset(buf[1])
			if !isFromServer && n >= probePayOff+8 && buf[probePayOff] == 0x00 && buf[probePayOff+1] == 0x01 {
				if !createKCPReceived {
					createKCPReceived = true
					firstCreateKCPTime = time.Now()
					log.Printf("%s CREATE_KCP received — waiting 3s for TRANSFER", s.prefix)
				}
				// Send meter ACK
				respPayload := BuildMeterAckFromRequest(buf[probePayOff:minInt(n, probePayOff+68)])
				respFrame := BuildMTPFrame(respPayload, true)
				if fromAddr != nil {
					pc.WriteToUDP(respFrame, fromAddr)
				}
				for _, addr := range s.lanMTPAddrs {
					pc.WriteToUDP(respFrame, addr)
				}
				// Send to relay servers
				for _, ra := range s.relayAddrs {
					raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ra.IP, ra.Port))
					if raddr != nil {
						extFrame := BuildExtendedMTPFrame(respPayload, s.targetDev.TID, true)
						pc.WriteToUDP(extFrame, raddr)
					}
				}

				if !kcpSessionSent {
					kcpSessionSent = true
					earlyKCP := BuildCreateKCPSessionMsg(s.linkID, token.AccessID, s.targetDev.TID)
					earlyFrame := BuildMTPFrame(earlyKCP, true)
					if fromAddr != nil {
						pc.WriteToUDP(earlyFrame, fromAddr)
					}
					for _, addr := range s.lanMTPAddrs {
						pc.WriteToUDP(earlyFrame, addr)
					}
					for _, ra := range s.relayAddrs {
						raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ra.IP, ra.Port))
						if raddr != nil {
							extFrame := BuildExtendedMTPFrame(earlyKCP, s.targetDev.TID, true)
							pc.WriteToUDP(extFrame, raddr)
						}
					}
				}
			}
		} else {
			decrypted, _ := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
			if decrypted != nil {
				sub := decrypted[1]
				if sub == 0xCB || sub == 0x02 {
					if !isFromServer && !createKCPReceived {
						s.addLanMTPAddr(fromAddr)
						createKCPReceived = true
						firstCreateKCPTime = time.Now()
						kcpSessionSent = true
						earlyKCP := BuildCreateKCPSessionMsg(s.linkID, token.AccessID, s.targetDev.TID)
						earlyFrame := BuildMTPFrame(earlyKCP, true)
						for _, addr := range s.lanMTPAddrs {
							pc.WriteToUDP(earlyFrame, addr)
						}
					}
				}
				if sub == 0xCA && !isFromServer {
					resp := BuildPortStatResp(token, s.routingSessionID, s.nextSqnum(),
						s.linkID, s.targetDev.TID, s.pwdKey)
					pc.WriteToUDP(resp, fromAddr)
					s.addLanMTPAddr(fromAddr)
				}
			}
		}
	}
}

// isCameraLanIP returns true if the IP is on a real LAN subnet (10.0.0.0/8 or 192.168.0.0/16).
// Docker bridge (172.16.0.0/12) is explicitly excluded — those IPs cannot reach the camera.
func isCameraLanIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// 10.0.0.0/8 — cameras are on 10.10.x.x
	if ip4[0] == 10 {
		return true
	}
	// 192.168.0.0/16
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	// 172.16.0.0/12 is intentionally excluded (Docker bridge)
	return false
}

// addLanMTPAddr adds a new LAN MTP address if not already tracked.
// Only private/LAN IPs are accepted — public relay server IPs are filtered out.
func (s *Session) addLanMTPAddr(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	if !isCameraLanIP(addr.IP) {
		return
	}
	for _, a := range s.lanMTPAddrs {
		if a.IP.Equal(addr.IP) && a.Port == addr.Port {
			return
		}
	}
	newAddr := &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port}
	s.lanMTPAddrs = append(s.lanMTPAddrs, newAddr)
	log.Printf("%s discovered camera MTP addr: %s", s.prefix, newAddr)
}

// setupTransport configures the MTP transport path (LAN/relay/TCP).
func (s *Session) setupTransport(callingRelayIP net.IP, callingRelayPort uint16,
	peerOuterIP net.IP, peerOuterPort uint16, tcpRelayChan chan net.Conn) {

	// Build UDP relay targets
	if len(s.relayAddrs) > 0 {
		seen := make(map[netip.AddrPort]bool)
		for _, ra := range s.relayAddrs {
			addr, err := netip.ParseAddr(ra.IP.String())
			if err != nil {
				continue
			}
			ap := netip.AddrPortFrom(addr, ra.Port)
			if seen[ap] {
				continue
			}
			seen[ap] = true
			raddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ra.IP, ra.Port))
			if raddr != nil {
				s.udpRelayTargets = append(s.udpRelayTargets, UDPRelayTarget{Addr: raddr, SessionID: ra.SessionID})
			}
		}
	}

	// Prefer LAN direct when camera is reachable on LAN.
	// TCP relay goes through Wyze's servers and has session timeouts (~2-3 min).
	if len(s.lanMTPAddrs) > 0 {
		// Close any pending TCP relay — we don't need it
		select {
		case c := <-tcpRelayChan:
			if c != nil {
				c.Close()
			}
		default:
		}
		log.Printf("%s using LAN direct (%d addrs) for MTP", s.prefix, len(s.lanMTPAddrs))
	} else {
		// No LAN addresses — wait for TCP relay as fallback
		select {
		case c := <-tcpRelayChan:
			s.tcpRelay = c
		case <-time.After(5 * time.Second):
		}
		if s.tcpRelay != nil {
			log.Printf("%s using TCP relay for MTP", s.prefix)
		} else {
			log.Printf("%s using P2P server relay for MTP", s.prefix)
		}
	}
}

// sendMTP sends an MTP frame to the camera via the best available path.
// Like kcpOutputFn, prefers LAN direct and avoids flooding the P2P server.
func (s *Session) sendMTP(data []byte) {
	// If we have a confirmed best LAN address, use only that
	if s.bestLanAddr != nil {
		s.pc.WriteToUDP(data, s.bestLanAddr)
		return
	}

	// Send to all known camera LAN addresses (during initial setup/probing)
	if len(s.lanMTPAddrs) > 0 {
		for _, addr := range s.lanMTPAddrs {
			s.pc.WriteToUDP(data, addr)
		}
		return
	}

	// Fallback: no LAN addresses, use relay paths
	if s.tcpRelay != nil {
		tcpData := make([]byte, len(data))
		copy(tcpData, data)
		if len(tcpData) > 1 {
			tcpData[1] &^= 0x10
		}
		s.tcpRelay.SetWriteDeadline(time.Now().Add(3 * time.Second))
		s.tcpRelay.Write(tcpData)
	}

	if len(data) > 6 && data[0] == 0xC0 {
		payload := data[6:]
		urgent := data[1]&0x80 != 0
		for _, rt := range s.udpRelayTargets {
			extFrame := BuildExtendedMTPFrame(payload, s.targetDev.TID, urgent)
			s.pc.WriteToUDP(extFrame, rt.Addr)
		}
	}
}

// streamLoop sets up KCP sessions and streams H.264 video.
func (s *Session) streamLoop() error {
	token := s.cfg.Token
	result := s.certResult
	pc := s.pc
	buf := make([]byte, 8192)

	// Send heartbeat
	hb := BuildHeartbeat(token, s.routingSessionID, s.nextSqnum(), result.SessionKey, s.pwdKey)
	pc.WriteToUDP(hb, s.serverUDPAddr)

	// PASSTHROUGH CONTROL to trigger KCP creation
	ptCtrl := BuildPassthroughControl(token, s.routingSessionID, s.nextSqnum(),
		s.targetDev.TID, 0x01, s.linkID, result.SessionKey, s.pwdKey)
	pc.WriteToUDP(ptCtrl, s.serverUDPAddr)
	time.Sleep(200 * time.Millisecond)

	// Create KCP session
	kcpCreateMsg := BuildCreateKCPSessionMsg(s.linkID, token.AccessID, s.targetDev.TID)
	mtpFrame := BuildMTPFrame(kcpCreateMsg, true)
	s.sendMTP(mtpFrame)

	s.convData = s.linkID & 0x7FFFFFFF
	s.convCtrl = s.linkID | 0x80000000
	log.Printf("%s KCP: data=0x%08X ctrl=0x%08X", s.prefix, s.convData, s.convCtrl)

	// KCP output function — SDK sends to ONE destination based on channel type.
	// LAN (type 0x01): send ONLY to camera's LAN address.
	// Sending to P2P server causes session death after ~3 minutes (server can't
	// parse raw MTP frames, treats session as misbehaving).
	kcpOutputFn := func(data []byte, size int) {
		payload := data[:size]
		stdFrame := BuildMTPFrame(payload, false)

		// If we have a confirmed best LAN address (from received data), use only that
		if s.bestLanAddr != nil {
			s.pc.WriteToUDP(stdFrame, s.bestLanAddr)
			return
		}

		// Otherwise send to all known camera LAN addresses (during initial probing)
		if len(s.lanMTPAddrs) > 0 {
			for _, addr := range s.lanMTPAddrs {
				s.pc.WriteToUDP(stdFrame, addr)
			}
			return
		}

		// Fallback: no LAN addresses available, use relay paths
		for _, rt := range s.udpRelayTargets {
			extFrame := BuildExtendedMTPFrame(payload, s.targetDev.TID, false)
			s.pc.WriteToUDP(extFrame, rt.Addr)
		}
		if s.tcpRelay != nil {
			tcpData := make([]byte, len(stdFrame))
			copy(tcpData, stdFrame)
			if len(tcpData) > 1 {
				tcpData[1] &^= 0x10
			}
			s.tcpRelay.Write(tcpData)
		}
	}

	s.dataKCP = NewKCPConn(s.convData, kcpOutputFn)
	s.ctrlKCP = NewKCPConn(s.convCtrl, kcpOutputFn)
	s.dataKCP.NoDelay(0, 5, 10, 1)
	s.ctrlKCP.NoDelay(0, 5, 10, 1)
	s.dataKCP.SetWndSize(64, 512)
	s.ctrlKCP.SetWndSize(64, 128)
	s.dataKCP.LogPrefix = s.prefix + " DATA"
	s.ctrlKCP.LogPrefix = s.prefix + " CTRL"

	// KCP update goroutine
	kcpDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-kcpDone:
				return
			case <-ticker.C:
				now := uint32(time.Now().UnixMilli() & 0xFFFFFFFF)
				s.dataKCP.Update(now)
				s.ctrlKCP.Update(now)
			}
		}
	}()
	defer close(kcpDone)

	// INITREQ retry loop
	streamID := mathrand.Uint32()
	if streamID == 0 {
		streamID = 1
	}
	avKey := make([]byte, 32)
	binary.LittleEndian.PutUint32(avKey[0:4], 2)
	initreqPayload := BuildAVStreamCtlINITREQ(streamID, 1, 1, 0, avKey)

	kcpSN := uint32(0)
	acceptReceived := false
	initDeadline := time.Now().Add(60 * time.Second)

	for retry := 0; !acceptReceived && time.Now().Before(initDeadline); retry++ {
		// Check for late TCP relay
		if s.tcpRelay == nil {
			select {
			case c := <-make(chan net.Conn): // non-blocking check handled below
				if c != nil {
					s.tcpRelay = c
				}
			default:
			}
		}

		ts := uint32(time.Now().UnixMilli() & 0xFFFFFFFF)
		if kcpSN > 0 {
			retransmit := BuildKCPPushSegment(s.convCtrl, 0, ts, initreqPayload)
			s.sendMTP(BuildMTPFrame(retransmit, false))
		}
		kcpSegment := BuildKCPPushSegment(s.convCtrl, kcpSN, ts, initreqPayload)
		s.sendMTP(BuildMTPFrame(kcpSegment, false))
		kcpSN++

		if retry == 0 {
			log.Printf("%s sent INITREQ via ctrl KCP", s.prefix)
		} else if retry%10 == 0 {
			log.Printf("%s INITREQ retry %d (%.0fs left)", s.prefix, retry, time.Until(initDeadline).Seconds())
		}

		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			remaining := time.Until(deadline)
			if remaining < 10*time.Millisecond {
				remaining = 10 * time.Millisecond
			}
			pc.SetReadDeadline(time.Now().Add(remaining))
			n, fromAddr, readErr := pc.ReadFromUDP(buf)
			if readErr != nil {
				break
			}

			isFromServer := fromAddr.IP.Equal(s.serverUDPAddr.IP) && fromAddr.Port == s.serverUDPAddr.Port

			if buf[0] == 0xC0 {
				if !isFromServer {
					mtpFlags := buf[1]
					if (mtpFlags>>5)&3 != 0 {
						addrKey := fromAddr.String()
						newAddr := &net.UDPAddr{IP: append(net.IP(nil), fromAddr.IP...), Port: fromAddr.Port}
						s.discoveredRelays.LoadOrStore(addrKey, newAddr)
					} else {
						s.addLanMTPAddr(fromAddr)
						// Lock best LAN addr during INITREQ phase too
						if isCameraLanIP(fromAddr.IP) && s.bestLanAddr == nil {
							s.bestLanAddr = &net.UDPAddr{IP: append(net.IP(nil), fromAddr.IP...), Port: fromAddr.Port}
							log.Printf("%s locked bestLanAddr: %s", s.prefix, s.bestLanAddr)
						}
					}
				}

				var extractedKey []byte
				avCmd := ParseMTPForAVSTREAMCTL(buf, n, s.convCtrl, s.convData, &extractedKey)
				FeedMTPToKCP(buf, n, s.dataKCP, s.ctrlKCP, s.convData, s.convCtrl)

				if avCmd == 2 {
					acceptReceived = true
					log.Printf("%s ACCEPT received!", s.prefix)
					break
				}
				// Handle meter req
				if avCmd == 0x10001 {
					payOff := MTPPayloadOffset(buf[1])
					resp := BuildMeterAckFromRequest(buf[payOff:minInt(n, payOff+68)])
					respFrame := BuildMTPFrame(resp, true)
					if fromAddr != nil {
						pc.WriteToUDP(respFrame, fromAddr)
					}
					for _, addr := range s.lanMTPAddrs {
						pc.WriteToUDP(respFrame, addr)
					}
				}
			} else {
				decrypted, _ := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
				if decrypted != nil && decrypted[1] == 0xCA {
					resp := BuildPortStatResp(token, s.routingSessionID, s.nextSqnum(),
						s.linkID, s.targetDev.TID, s.pwdKey)
					pc.WriteToUDP(resp, fromAddr)
				}
			}
		}

		if retry%5 == 4 {
			hb := BuildHeartbeat(token, s.routingSessionID, s.nextSqnum(), result.SessionKey, s.pwdKey)
			pc.WriteToUDP(hb, s.serverUDPAddr)
		}
	}

	if !acceptReceived {
		return fmt.Errorf("INITREQ: no ACCEPT after 60s")
	}

	// Send START via DATA KCP
	log.Printf("%s sending START via DATA KCP", s.prefix)
	startPayload := BuildAVStreamCtlSTART(streamID)
	s.dataKCP.Send(startPayload)
	s.dataKCP.Flush()

	// Send viewer init_video_info
	avKeyInit := make([]byte, 32)
	avKeyInit[0] = 0x04
	avKeyInit[1] = 0x02
	binary.LittleEndian.PutUint16(avKeyInit[2:4], 0x0020)
	for i := 0; i < 3; i++ {
		off := 4 + i*8
		s.mtpRC5Ctx.EncryptBlock(avKeyInit[off : off+8])
	}
	s.dataKCP.Send(avKeyInit)
	s.dataKCP.Flush()

	// Main stream loop
	s.state = StateStreaming
	log.Printf("%s streaming started", s.prefix)

	lastHeartbeat := time.Now()
	lastStatus := time.Now()
	lastOnlineSocket := time.Now()
	lastMeterProbe := time.Now()

	for !s.isClosed() {
		// Heartbeat every 40 seconds (SDK uses 35-50s)
		if time.Since(lastHeartbeat) > 40*time.Second {
			hb := BuildHeartbeat(token, s.routingSessionID, s.nextSqnum(), result.SessionKey, s.pwdKey)
			pc.WriteToUDP(hb, s.serverUDPAddr)
			lastHeartbeat = time.Now()
		}

		// Meter probe every 2 seconds — keeps camera session alive
		// SDK: iv_mtpSession_send_meter_proc sends these periodically.
		// Camera kills session if no meter probes arrive for ~119 seconds.
		if time.Since(lastMeterProbe) > 2*time.Second {
			s.meterRound++
			probe := BuildMeterProbe(s.linkID, token.AccessID, s.targetDev.TID, s.meterRound)
			probeFrame := BuildMTPFrame(probe, true)
			s.sendMTP(probeFrame)
			if s.meterRound%15 == 0 {
				log.Printf("%s meter probe round=%d", s.prefix, s.meterRound)
			}
			lastMeterProbe = time.Now()
		}

		// Online socket keepalive every 10 seconds — tells P2P server we're still alive
		// SDK: gat_send_online_socket @ 0x148d74 sends 0xCA sub_type=3 to P2P server (param_1+0x5c)
		if time.Since(lastOnlineSocket) > 10*time.Second {
			keepalive := BuildSessionSocket(token, s.routingSessionID, s.nextSqnum(),
				s.linkID, s.targetDev.TID, 3, nil, s.pwdKey)
			pc.WriteToUDP(keepalive, s.serverUDPAddr)
			lastOnlineSocket = time.Now()
		}

		// Status log every 30 seconds — enhanced with KCP diagnostics
		if time.Since(lastStatus) > 30*time.Second {
			ds := s.dataKCP.Stats()
			cs := s.ctrlKCP.Stats()
			log.Printf("%s STREAM: %d pkts, %d bytes | rawUDP: %d", s.prefix, s.streamPkts, s.streamDataBytes, s.rawUDPPkts)
			log.Printf("%s  DATA-KCP: rcvNxt=%d rcvQ=%d rcvBuf=%d sndUna=%d sndNxt=%d waitSnd=%d rmtWnd=%d cwnd=%d rexmit=%d dropOld=%d dropAhead=%d lastDropSN=%d",
				s.prefix, ds.RcvNxt, ds.RcvQueueLen, ds.RcvBufLen, ds.SndUna, ds.SndNxt, ds.SndBufLen+ds.SndQueueLen, ds.RmtWnd, ds.CWnd, ds.Retransmits, ds.SNDropOld, ds.SNDropAhead, ds.LastDropSN)
			log.Printf("%s  CTRL-KCP: rcvNxt=%d rcvQ=%d rcvBuf=%d sndUna=%d sndNxt=%d waitSnd=%d rmtWnd=%d cwnd=%d rexmit=%d dropOld=%d dropAhead=%d",
				s.prefix, cs.RcvNxt, cs.RcvQueueLen, cs.RcvBufLen, cs.SndUna, cs.SndNxt, cs.SndBufLen+cs.SndQueueLen, cs.RmtWnd, cs.CWnd, cs.Retransmits, cs.SNDropOld, cs.SNDropAhead)
			meterAge := "never"
			if !s.lastMeterRecv.IsZero() {
				meterAge = fmt.Sprintf("%.1fs ago", time.Since(s.lastMeterRecv).Seconds())
			}
			log.Printf("%s  meter: sent=%d recvREQ=%d recvACK=%d lastRecv=%s | bestLAN: %v",
				s.prefix, s.meterRound, s.meterReqCount, s.meterAckCount, meterAge, s.bestLanAddr)
			s.meterReqCount = 0
			s.meterAckCount = 0
			lastStatus = time.Now()
		}

		pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, fromAddr, readErr := pc.ReadFromUDP(buf)
		if readErr != nil {
			// Check KCP receive queues even on timeout
			s.drainKCPRecv()
			continue
		}
		s.rawUDPPkts++

		isFromServer := fromAddr.IP.Equal(s.serverUDPAddr.IP) && fromAddr.Port == s.serverUDPAddr.Port

		if buf[0] == 0xC0 {
			s.lastDataFrom = fromAddr.String()
			// Track source addresses and update best LAN addr
			if !isFromServer && fromAddr != nil {
				mtpFlags := buf[1]
				if (mtpFlags>>5)&3 != 0 {
					addrKey := fromAddr.String()
					newAddr := &net.UDPAddr{IP: append(net.IP(nil), fromAddr.IP...), Port: fromAddr.Port}
					s.discoveredRelays.LoadOrStore(addrKey, newAddr)
				} else {
					s.addLanMTPAddr(fromAddr)
					// Lock onto the LAN address that's actually sending us data
					// SDK equivalent: mtpSession+0x5c (best addr from meter probing)
					if isCameraLanIP(fromAddr.IP) && s.bestLanAddr == nil {
						s.bestLanAddr = &net.UDPAddr{IP: append(net.IP(nil), fromAddr.IP...), Port: fromAddr.Port}
						log.Printf("%s locked bestLanAddr: %s", s.prefix, s.bestLanAddr)
					}
				}
			}

			// Track incoming meter frames (summarized in 30s status log)
			{
				diagOff := MTPPayloadOffset(buf[1])
				if n >= diagOff+4 && buf[diagOff] == 0x00 {
					mCmd := buf[diagOff+1]
					if mCmd == 0x01 {
						s.meterReqCount++
						s.lastMeterRecv = time.Now()
					} else if mCmd == 0x02 {
						s.meterAckCount++
						s.lastMeterRecv = time.Now()
					}
				}
			}

			// Feed MTP to KCP
			sessCmd := FeedMTPToKCP(buf, n, s.dataKCP, s.ctrlKCP, s.convData, s.convCtrl)

			// Direct ACK for every PUSH (helps with reliability)
			{
				mPayOff := MTPPayloadOffset(buf[1])
				if n >= mPayOff+8 && buf[mPayOff] == 0x00 {
					mcmd := buf[mPayOff+1]
					if mcmd == 0x01 || mcmd == 0x02 || mcmd == 0x04 || mcmd == 0x06 || mcmd == 0x21 || mcmd == 0x24 || mcmd == 0x25 {
						msLen := binary.LittleEndian.Uint16(buf[mPayOff+2 : mPayOff+4])
						mPayOff += int(msLen)
					}
				}
				off := mPayOff
				for off+24 <= n {
					kcpCmd := buf[off+4]
					if kcpCmd == 81 {
						kcpConv := binary.LittleEndian.Uint32(buf[off : off+4])
						kcpSN2 := binary.LittleEndian.Uint32(buf[off+12 : off+16])
						kcpTS := binary.LittleEndian.Uint32(buf[off+8 : off+12])
						ackSeg := BuildKCPAckSegment(kcpConv, kcpSN2, kcpTS, s.dataKCP.RcvNxt())
						ackMTP := BuildMTPFrame(ackSeg, false)
						if fromAddr != nil {
							pc.WriteToUDP(ackMTP, fromAddr)
						}
					}
					segLen := binary.LittleEndian.Uint32(buf[off+20 : off+24])
					off += 24 + int(segLen)
				}
			}

			// Handle meter req — camera sent us a meter probe, we respond with ACK
			if sessCmd == 0x10001 {
				payOff := MTPPayloadOffset(buf[1])
				resp := BuildMeterAckFromRequest(buf[payOff:minInt(n, payOff+68)])
				respFrame := BuildMTPFrame(resp, true)
				if fromAddr != nil {
					pc.WriteToUDP(respFrame, fromAddr)
				}
				for _, addr := range s.lanMTPAddrs {
					pc.WriteToUDP(respFrame, addr)
				}
			}

			// Drain KCP receive queues
			s.drainKCPRecv()
		} else {
			decrypted, _ := TryDecrypt(buf, n, result.SessionKey, s.pwdKey)
			if decrypted != nil {
				sub := decrypted[1]
				if sub == 0xCA {
					resp := BuildPortStatResp(token, s.routingSessionID, s.nextSqnum(),
						s.linkID, s.targetDev.TID, s.pwdKey)
					pc.WriteToUDP(resp, fromAddr)
				}
				// Handle PASSTHROUGH DATA containing MTP
				if sub == 0xB9 && n >= 52 {
					modeFlags := binary.LittleEndian.Uint32(decrypted[24:28])
					if modeFlags&1 == 0 {
						ptPayloadLen := binary.LittleEndian.Uint16(decrypted[48:50])
						if int(52+ptPayloadLen) <= n && ptPayloadLen > 0 {
							ptPayload := decrypted[52 : 52+ptPayloadLen]
							if ptPayload[0] == 0xC0 && ptPayloadLen >= 6 {
								copy(buf[:ptPayloadLen], ptPayload)
								FeedMTPToKCP(buf, int(ptPayloadLen), s.dataKCP, s.ctrlKCP, s.convData, s.convCtrl)
							}
						}
					}
				}
			}
		}
	}

	// Death diagnostics — log full KCP state when stream ends
	ds := s.dataKCP.Stats()
	cs := s.ctrlKCP.Stats()
	meterAge := "never"
	if !s.lastMeterRecv.IsZero() {
		meterAge = fmt.Sprintf("%.1fs ago", time.Since(s.lastMeterRecv).Seconds())
	}
	log.Printf("%s STREAM ENDED — death diagnostics:", s.prefix)
	log.Printf("%s  STREAM: %d pkts, %d bytes | rawUDP: %d", s.prefix, s.streamPkts, s.streamDataBytes, s.rawUDPPkts)
	log.Printf("%s  DATA-KCP: rcvNxt=%d rcvQ=%d rcvBuf=%d sndUna=%d sndNxt=%d waitSnd=%d rmtWnd=%d cwnd=%d rexmit=%d dropOld=%d dropAhead=%d lastDropSN=%d",
		s.prefix, ds.RcvNxt, ds.RcvQueueLen, ds.RcvBufLen, ds.SndUna, ds.SndNxt, ds.SndBufLen+ds.SndQueueLen, ds.RmtWnd, ds.CWnd, ds.Retransmits, ds.SNDropOld, ds.SNDropAhead, ds.LastDropSN)
	log.Printf("%s  CTRL-KCP: rcvNxt=%d rcvQ=%d rcvBuf=%d sndUna=%d sndNxt=%d waitSnd=%d rmtWnd=%d cwnd=%d rexmit=%d dropOld=%d dropAhead=%d",
		s.prefix, cs.RcvNxt, cs.RcvQueueLen, cs.RcvBufLen, cs.SndUna, cs.SndNxt, cs.SndBufLen+cs.SndQueueLen, cs.RmtWnd, cs.CWnd, cs.Retransmits, cs.SNDropOld, cs.SNDropAhead)
	log.Printf("%s  meter: sent=%d lastRecv=%s | bestLAN: %v | lastDataFrom: %s",
		s.prefix, s.meterRound, meterAge, s.bestLanAddr, s.lastDataFrom)
	return nil
}

// drainKCPRecv processes all available data from KCP receive queues.
func (s *Session) drainKCPRecv() {
	for {
		data := s.dataKCP.Recv()
		if data == nil {
			break
		}
		s.streamPkts++
		s.streamDataBytes += len(data)
		if len(data) > 0 {
			h264 := DecryptMTPPayload(data, s.mtpRC5Ctx, "DATA")
			if h264 != nil && s.cfg.H264Writer != nil {
				s.cfg.H264Writer.Write(h264)
			}
		}
	}
	for {
		data := s.ctrlKCP.Recv()
		if data == nil {
			break
		}
		if len(data) > 0 {
			if len(data) >= 4 {
				log.Printf("%s CTRL-KCP recv: type=0x%02X len=%d first8=%x",
					s.prefix, data[0], len(data), data[:minInt(8, len(data))])
			}
			// Parse AVSTREAMCTL messages (type=0x03)
			if len(data) >= 12 && data[0] == 0x03 {
				avAction := binary.LittleEndian.Uint32(data[8:12])
				avActionName := fmt.Sprintf("0x%X", avAction)
				switch avAction {
				case 1:
					avActionName = "INITREQ"
				case 2:
					avActionName = "ACCEPT"
				case 3:
					avActionName = "REJECT"
				case 4:
					avActionName = "STOP"
				case 5:
					avActionName = "CLOSE"
				case 6:
					avActionName = "START"
				}
				reason := uint32(0)
				if len(data) >= 16 {
					reason = binary.LittleEndian.Uint32(data[12:16])
				}
				log.Printf("%s AVSTREAMCTL on CTRL: action=%s(%d) reason=%d len=%d hex=%x",
					s.prefix, avActionName, avAction, reason, len(data), data[:minInt(len(data), 40)])
			}
			DecryptMTPPayload(data, s.mtpRC5Ctx, "CTRL")
		}
	}
}
