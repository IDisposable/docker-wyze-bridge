package gwell

// Minimal KCP (ARQ protocol) implementation for the GWell P2P camera protocol.
// Based on the standard KCP protocol by skywind3000.
// Only implements what's needed: receive path, ACK generation, WASK/WINS, basic send.

import (
	"encoding/binary"
	"log"
	"sync"
)

const (
	IKCP_CMD_PUSH = 81
	IKCP_CMD_ACK  = 82
	IKCP_CMD_WASK = 83
	IKCP_CMD_WINS = 84

	IKCP_OVERHEAD  = 24
	IKCP_MTU_DEF   = 1400
	IKCP_WND_SND   = 32
	IKCP_WND_RCV   = 128
	IKCP_INTERVAL  = 10 // ms — aggressive for video streaming
	IKCP_RTO_DEF   = 200
	IKCP_RTO_MIN   = 100
	IKCP_RTO_MAX   = 60000
	IKCP_ASK_SEND  = 1
	IKCP_ASK_TELL  = 2
	IKCP_DEADLINK  = 20
	IKCP_THRESH_INIT = 2
	IKCP_PROBE_INIT  = 7000
	IKCP_PROBE_LIMIT = 120000
)

type kcpSeg struct {
	conv uint32
	cmd  uint8
	frg  uint8
	wnd  uint16
	ts   uint32
	sn   uint32
	una  uint32
	data []byte
	// retransmit state
	resendts uint32
	rto      uint32
	fastack  uint32
	xmit     uint32
}

// KCPConn is a minimal KCP connection instance.
type KCPConn struct {
	conv uint32
	mu   sync.Mutex

	// Send state
	sndUna uint32
	sndNxt uint32

	// Receive state
	rcvNxt uint32

	// Windows
	sndWnd uint32
	rcvWnd uint32
	rmtWnd uint32

	// MTU
	mtu uint32
	mss uint32

	// Timing
	current  uint32
	interval uint32
	tsFlush  uint32
	updated  bool

	// RTT
	rxSrtt  int32
	rxRttval int32
	rxRto   int32
	rxMinRto int32

	// Congestion
	cwnd    uint32
	ssthresh uint32

	// Buffers
	sndQueue []*kcpSeg
	sndBuf   []*kcpSeg
	rcvBuf   []*kcpSeg
	rcvQueue []*kcpSeg

	// ACK list: pairs of (sn, ts)
	ackList []uint32

	// Window probe
	probe     uint32
	tsProbe   uint32
	probeWait uint32

	// Dead link detection
	deadLink uint32

	// NoDelay config
	nodelay  int32
	fastResend int32
	nocwnd   int32

	// Output callback — sends raw KCP data (one or more segments) to the network
	Output func(data []byte, size int)

	// Encoding buffer
	buffer []byte

	// Diagnostics
	retransmits  uint32 // total retransmit count
	snDropOld    uint32 // segments dropped: SN < rcvNxt
	snDropAhead  uint32 // segments dropped: SN >= rcvNxt + rcvWnd
	lastDropSN   uint32 // last dropped SN (for diagnostics)
	LogPrefix    string
}

// NewKCPConn creates a new KCP connection with the given conv ID and output callback.
func NewKCPConn(conv uint32, output func(data []byte, size int)) *KCPConn {
	kcp := &KCPConn{
		conv:      conv,
		sndWnd:    IKCP_WND_SND,
		rcvWnd:    IKCP_WND_RCV,
		rmtWnd:    IKCP_WND_RCV,
		mtu:       IKCP_MTU_DEF,
		mss:       IKCP_MTU_DEF - IKCP_OVERHEAD,
		interval:  IKCP_INTERVAL,
		rxRto:     IKCP_RTO_DEF,
		rxMinRto:  IKCP_RTO_MIN,
		ssthresh:  IKCP_THRESH_INIT,
		deadLink:  IKCP_DEADLINK,
		Output:    output,
		buffer:    make([]byte, (IKCP_MTU_DEF+IKCP_OVERHEAD)*3),
	}
	return kcp
}

// SetMTU configures the MTU.
func (kcp *KCPConn) SetMTU(mtu int) {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	if mtu < 50 || mtu < IKCP_OVERHEAD {
		return
	}
	kcp.mtu = uint32(mtu)
	kcp.mss = kcp.mtu - IKCP_OVERHEAD
	buf := make([]byte, (kcp.mtu+IKCP_OVERHEAD)*3)
	kcp.buffer = buf
}

// SetWndSize sets the send and receive window sizes.
func (kcp *KCPConn) SetWndSize(sndWnd, rcvWnd int) {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	if sndWnd > 0 {
		kcp.sndWnd = uint32(sndWnd)
	}
	if rcvWnd > 0 {
		kcp.rcvWnd = uint32(rcvWnd)
	}
}

// NoDelay configures fast mode settings.
// nodelay: 0=normal, 1=nodelay
// interval: internal update interval (ms), default 100, recommend 10-40
// resend: fast resend trigger count, default 0 (disabled), recommend 2
// nc: 0=normal congestion control, 1=no congestion control
func (kcp *KCPConn) NoDelay(nodelay, interval, resend, nc int) {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	if nodelay >= 0 {
		kcp.nodelay = int32(nodelay)
		if nodelay != 0 {
			kcp.rxMinRto = 30 // faster min RTO in nodelay mode
		} else {
			kcp.rxMinRto = IKCP_RTO_MIN
		}
	}
	if interval >= 0 {
		if interval > 5000 {
			interval = 5000
		} else if interval < 10 {
			interval = 10
		}
		kcp.interval = uint32(interval)
	}
	if resend >= 0 {
		kcp.fastResend = int32(resend)
	}
	if nc >= 0 {
		kcp.nocwnd = int32(nc)
	}
}

// encodeSeg writes a KCP segment header to buf and returns bytes written.
func encodeSeg(buf []byte, seg *kcpSeg) int {
	binary.LittleEndian.PutUint32(buf[0:4], seg.conv)
	buf[4] = seg.cmd
	buf[5] = seg.frg
	binary.LittleEndian.PutUint16(buf[6:8], seg.wnd)
	binary.LittleEndian.PutUint32(buf[8:12], seg.ts)
	binary.LittleEndian.PutUint32(buf[12:16], seg.sn)
	binary.LittleEndian.PutUint32(buf[16:20], seg.una)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(len(seg.data)))
	return 24
}

// Input processes raw KCP data received from the network.
// The data may contain multiple KCP segments concatenated.
func (kcp *KCPConn) Input(data []byte) int {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()

	if len(data) < IKCP_OVERHEAD {
		return -1
	}

	var maxACK uint32
	hasACK := false

	for len(data) >= IKCP_OVERHEAD {
		conv := binary.LittleEndian.Uint32(data[0:4])
		if conv != kcp.conv {
			return -1
		}
		cmd := data[4]
		frg := data[5]
		wnd := binary.LittleEndian.Uint16(data[6:8])
		ts := binary.LittleEndian.Uint32(data[8:12])
		sn := binary.LittleEndian.Uint32(data[12:16])
		una := binary.LittleEndian.Uint32(data[16:20])
		length := binary.LittleEndian.Uint32(data[20:24])

		data = data[IKCP_OVERHEAD:]
		if uint32(len(data)) < length {
			return -2
		}

		if cmd != IKCP_CMD_PUSH && cmd != IKCP_CMD_ACK &&
			cmd != IKCP_CMD_WASK && cmd != IKCP_CMD_WINS {
			return -3
		}

		kcp.rmtWnd = uint32(wnd)

		// Process UNA — remove all segments from sndBuf with sn < una
		kcp.parseUna(una)

		// Shrink sndUna
		kcp.shrinkBuf()

		switch cmd {
		case IKCP_CMD_ACK:
			rtt := itimediff(kcp.current, ts)
			if rtt >= 0 {
				kcp.updateRTO(rtt)
			}
			kcp.parseACK(sn)
			kcp.shrinkBuf()
			if !hasACK || itimediff(sn, maxACK) > 0 {
				hasACK = true
				maxACK = sn
			}

		case IKCP_CMD_PUSH:
			if itimediff(sn, kcp.rcvNxt+kcp.rcvWnd) < 0 {
				// Queue ACK for this segment
				kcp.ackList = append(kcp.ackList, sn, ts)

				if itimediff(sn, kcp.rcvNxt) >= 0 {
					seg := &kcpSeg{
						conv: conv, cmd: cmd, frg: frg, wnd: wnd,
						ts: ts, sn: sn, una: una,
					}
					if length > 0 {
						seg.data = make([]byte, length)
						copy(seg.data, data[:length])
					}
					kcp.parseData(seg)
				} else {
					// SN < rcvNxt — old/duplicate segment
					kcp.snDropOld++
					kcp.lastDropSN = sn
				}
			} else {
				// SN >= rcvNxt + rcvWnd — too far ahead
				kcp.snDropAhead++
				kcp.lastDropSN = sn
			}

		case IKCP_CMD_WASK:
			kcp.probe |= IKCP_ASK_TELL

		case IKCP_CMD_WINS:
			// rmtWnd already updated from header
		}

		data = data[length:]
	}

	// Fast ACK: mark segments in sndBuf for fast retransmit
	if hasACK {
		for _, seg := range kcp.sndBuf {
			if itimediff(seg.sn, kcp.sndUna) >= 0 && itimediff(seg.sn, maxACK) < 0 {
				seg.fastack++
			}
		}
	}

	return 0
}

// parseUna removes acked segments from sndBuf based on cumulative ACK.
func (kcp *KCPConn) parseUna(una uint32) {
	idx := 0
	for idx < len(kcp.sndBuf) {
		if itimediff(una, kcp.sndBuf[idx].sn) > 0 {
			idx++
		} else {
			break
		}
	}
	if idx > 0 {
		kcp.sndBuf = kcp.sndBuf[idx:]
	}
}

// parseACK removes a specific segment from sndBuf.
func (kcp *KCPConn) parseACK(sn uint32) {
	if itimediff(sn, kcp.sndUna) < 0 || itimediff(sn, kcp.sndNxt) >= 0 {
		return
	}
	for i, seg := range kcp.sndBuf {
		if sn == seg.sn {
			kcp.sndBuf = append(kcp.sndBuf[:i], kcp.sndBuf[i+1:]...)
			break
		}
		if itimediff(sn, seg.sn) < 0 {
			break
		}
	}
}

// shrinkBuf updates sndUna to the sn of the first unacked segment.
func (kcp *KCPConn) shrinkBuf() {
	if len(kcp.sndBuf) > 0 {
		kcp.sndUna = kcp.sndBuf[0].sn
	} else {
		kcp.sndUna = kcp.sndNxt
	}
}

// parseData inserts a received segment into rcvBuf (sorted), then moves
// consecutive segments from rcvBuf to rcvQueue.
func (kcp *KCPConn) parseData(newseg *kcpSeg) {
	sn := newseg.sn

	// Check for duplicate
	insertIdx := len(kcp.rcvBuf)
	for i := len(kcp.rcvBuf) - 1; i >= 0; i-- {
		if kcp.rcvBuf[i].sn == sn {
			return // duplicate
		}
		if itimediff(sn, kcp.rcvBuf[i].sn) > 0 {
			insertIdx = i + 1
			break
		}
		insertIdx = i
	}

	// Insert at position
	kcp.rcvBuf = append(kcp.rcvBuf, nil)
	copy(kcp.rcvBuf[insertIdx+1:], kcp.rcvBuf[insertIdx:])
	kcp.rcvBuf[insertIdx] = newseg

	// Move consecutive segments from rcvBuf to rcvQueue
	for len(kcp.rcvBuf) > 0 {
		seg := kcp.rcvBuf[0]
		if seg.sn == kcp.rcvNxt && uint32(len(kcp.rcvQueue)) < kcp.rcvWnd {
			kcp.rcvBuf = kcp.rcvBuf[1:]
			kcp.rcvQueue = append(kcp.rcvQueue, seg)
			kcp.rcvNxt++
		} else {
			break
		}
	}
}

// Recv retrieves received data. Returns the data or nil if no data available.
// For simplicity, returns one segment at a time (no fragment reassembly needed
// since the SDK uses frg=0 for all camera segments).
func (kcp *KCPConn) Recv() []byte {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()

	if len(kcp.rcvQueue) == 0 {
		return nil
	}

	// Check if we can assemble a complete message.
	// For fragmented messages, frg counts down: frg=2,1,0 means 3 fragments.
	// frg=0 on the last fragment. For our use case, most messages have frg=0.
	peekSize := 0
	for _, seg := range kcp.rcvQueue {
		peekSize += len(seg.data)
		if seg.frg == 0 {
			break
		}
	}
	if peekSize == 0 {
		return nil
	}

	// Assemble
	result := make([]byte, 0, peekSize)
	count := 0
	for _, seg := range kcp.rcvQueue {
		result = append(result, seg.data...)
		count++
		if seg.frg == 0 {
			break
		}
	}
	kcp.rcvQueue = kcp.rcvQueue[count:]

	// After removing from rcvQueue, try to move more from rcvBuf
	for len(kcp.rcvBuf) > 0 {
		seg := kcp.rcvBuf[0]
		if seg.sn == kcp.rcvNxt && uint32(len(kcp.rcvQueue)) < kcp.rcvWnd {
			kcp.rcvBuf = kcp.rcvBuf[1:]
			kcp.rcvQueue = append(kcp.rcvQueue, seg)
			kcp.rcvNxt++
		} else {
			break
		}
	}

	return result
}

// PeekSize returns the size of the next available message, or -1 if none.
func (kcp *KCPConn) PeekSize() int {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()

	if len(kcp.rcvQueue) == 0 {
		return -1
	}

	seg := kcp.rcvQueue[0]
	if seg.frg == 0 {
		return len(seg.data)
	}

	// Fragmented message
	if uint32(len(kcp.rcvQueue)) < uint32(seg.frg)+1 {
		return -1 // incomplete
	}

	size := 0
	for _, s := range kcp.rcvQueue {
		size += len(s.data)
		if s.frg == 0 {
			break
		}
	}
	return size
}

// Send queues data for sending. Data will be fragmented if larger than MSS.
func (kcp *KCPConn) Send(data []byte) int {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()

	if len(data) == 0 {
		return -1
	}

	count := 1
	if len(data) > int(kcp.mss) {
		count = (len(data) + int(kcp.mss) - 1) / int(kcp.mss)
	}

	if count > 255 {
		return -2 // too many fragments
	}

	for i := 0; i < count; i++ {
		size := int(kcp.mss)
		if len(data) < size {
			size = len(data)
		}
		seg := &kcpSeg{}
		seg.data = make([]byte, size)
		copy(seg.data, data[:size])
		seg.frg = uint8(count - i - 1) // fragment countdown
		kcp.sndQueue = append(kcp.sndQueue, seg)
		data = data[size:]
	}

	return 0
}

// Update is the main periodic update function. Call it regularly (e.g. every 10ms).
// current is the current time in milliseconds.
func (kcp *KCPConn) Update(current uint32) {
	kcp.mu.Lock()
	kcp.current = current
	if !kcp.updated {
		kcp.updated = true
		kcp.tsFlush = current
	}

	slap := itimediff(current, kcp.tsFlush)
	if slap > 10000 || slap < -10000 {
		kcp.tsFlush = current
		slap = 0
	}

	if slap >= 0 {
		kcp.tsFlush += kcp.interval
		if itimediff(current, kcp.tsFlush) >= 0 {
			kcp.tsFlush = current + kcp.interval
		}
		kcp.flushLocked()
	}
	kcp.mu.Unlock()
}

// Flush forces a flush of pending data and ACKs.
func (kcp *KCPConn) Flush() {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	kcp.flushLocked()
}

// flushLocked performs the actual flush. Must be called with kcp.mu held.
func (kcp *KCPConn) flushLocked() {
	if kcp.Output == nil {
		return
	}

	current := kcp.current
	buf := kcp.buffer
	offset := 0

	// Helper to write a segment to buffer and flush if near MTU
	flushBuf := func() {
		if offset > 0 {
			kcp.Output(buf[:offset], offset)
			offset = 0
		}
	}

	seg := &kcpSeg{conv: kcp.conv}

	// 1. Send ACKs
	for i := 0; i+1 < len(kcp.ackList); i += 2 {
		if offset+IKCP_OVERHEAD > int(kcp.mtu) {
			flushBuf()
		}
		seg.cmd = IKCP_CMD_ACK
		seg.sn = kcp.ackList[i]
		seg.ts = kcp.ackList[i+1]
		seg.una = kcp.rcvNxt
		seg.wnd = uint16(kcp.rcvWnd - uint32(len(kcp.rcvQueue)))
		if seg.wnd < 0 {
			seg.wnd = 0
		}
		seg.frg = 0
		seg.data = nil
		offset += encodeSeg(buf[offset:], seg)
	}
	kcp.ackList = kcp.ackList[:0]

	// 2. Window probe request (we need to know remote window if it's 0)
	if kcp.rmtWnd == 0 {
		if kcp.probeWait == 0 {
			kcp.probeWait = IKCP_PROBE_INIT
			kcp.tsProbe = current + kcp.probeWait
		} else if itimediff(current, kcp.tsProbe) >= 0 {
			if kcp.probeWait < IKCP_PROBE_INIT {
				kcp.probeWait = IKCP_PROBE_INIT
			}
			kcp.probeWait += kcp.probeWait / 2
			if kcp.probeWait > IKCP_PROBE_LIMIT {
				kcp.probeWait = IKCP_PROBE_LIMIT
			}
			kcp.tsProbe = current + kcp.probeWait
			kcp.probe |= IKCP_ASK_SEND
		}
	} else {
		kcp.tsProbe = 0
		kcp.probeWait = 0
	}

	// Send window probe (WASK)
	if kcp.probe&IKCP_ASK_SEND != 0 {
		if offset+IKCP_OVERHEAD > int(kcp.mtu) {
			flushBuf()
		}
		seg.cmd = IKCP_CMD_WASK
		seg.sn = 0
		seg.ts = current
		seg.una = kcp.rcvNxt
		seg.wnd = uint16(kcp.rcvWnd - uint32(len(kcp.rcvQueue)))
		seg.frg = 0
		seg.data = nil
		offset += encodeSeg(buf[offset:], seg)
	}

	// Send window response (WINS)
	if kcp.probe&IKCP_ASK_TELL != 0 {
		if offset+IKCP_OVERHEAD > int(kcp.mtu) {
			flushBuf()
		}
		seg.cmd = IKCP_CMD_WINS
		seg.sn = 0
		seg.ts = current
		seg.una = kcp.rcvNxt
		seg.wnd = uint16(kcp.rcvWnd - uint32(len(kcp.rcvQueue)))
		seg.frg = 0
		seg.data = nil
		offset += encodeSeg(buf[offset:], seg)
	}

	kcp.probe = 0

	// 3. Move data from sndQueue to sndBuf (within window)
	cwnd := min32(kcp.sndWnd, kcp.rmtWnd)
	if kcp.nocwnd == 0 {
		cwnd = min32(cwnd, kcp.cwnd)
	}

	for len(kcp.sndQueue) > 0 && itimediff(kcp.sndNxt, kcp.sndUna+cwnd) < 0 {
		newseg := kcp.sndQueue[0]
		kcp.sndQueue = kcp.sndQueue[1:]
		newseg.conv = kcp.conv
		newseg.cmd = IKCP_CMD_PUSH
		newseg.ts = current
		newseg.sn = kcp.sndNxt
		newseg.una = kcp.rcvNxt
		newseg.wnd = uint16(kcp.rcvWnd - uint32(len(kcp.rcvQueue)))
		newseg.resendts = current
		newseg.rto = uint32(kcp.rxRto)
		newseg.xmit = 0
		kcp.sndBuf = append(kcp.sndBuf, newseg)
		kcp.sndNxt++
	}

	// 4. Send/retransmit segments in sndBuf
	resent := uint32(0xFFFFFFFF)
	if kcp.fastResend > 0 {
		resent = uint32(kcp.fastResend)
	}

	change := false
	lost := false

	for _, segment := range kcp.sndBuf {
		needSend := false
		if segment.xmit == 0 {
			// First transmission
			needSend = true
			segment.xmit++
			segment.rto = uint32(kcp.rxRto)
			segment.resendts = current + segment.rto
		} else if itimediff(current, segment.resendts) >= 0 {
			// Timeout retransmission
			needSend = true
			segment.xmit++
			kcp.retransmits++
			if kcp.nodelay == 0 {
				segment.rto += uint32(kcp.rxRto)
			} else {
				segment.rto += uint32(kcp.rxRto) / 2
			}
			segment.resendts = current + segment.rto
			lost = true
		} else if segment.fastack >= resent {
			// Fast retransmission
			needSend = true
			segment.xmit++
			kcp.retransmits++
			segment.fastack = 0
			segment.resendts = current + segment.rto
			change = true
		}

		if needSend {
			segment.ts = current
			segment.una = kcp.rcvNxt
			segment.wnd = uint16(kcp.rcvWnd - uint32(len(kcp.rcvQueue)))

			need := IKCP_OVERHEAD + len(segment.data)
			if offset+need > int(kcp.mtu) {
				flushBuf()
			}

			offset += encodeSeg(buf[offset:], segment)
			if len(segment.data) > 0 {
				copy(buf[offset:], segment.data)
				offset += len(segment.data)
			}

			if segment.xmit >= kcp.deadLink {
				// dead link — too many retransmissions
				if kcp.LogPrefix != "" {
					log.Printf("%s KCP dead link detected: sn=%d xmit=%d", kcp.LogPrefix, segment.sn, segment.xmit)
				}
			}
		}
	}

	// Flush remaining buffer
	flushBuf()

	// 5. Update congestion window
	if change {
		inflight := kcp.sndNxt - kcp.sndUna
		kcp.ssthresh = inflight / 2
		if kcp.ssthresh < IKCP_THRESH_INIT {
			kcp.ssthresh = IKCP_THRESH_INIT
		}
		kcp.cwnd = kcp.ssthresh + resent
	}

	if lost {
		kcp.ssthresh = cwnd / 2
		if kcp.ssthresh < IKCP_THRESH_INIT {
			kcp.ssthresh = IKCP_THRESH_INIT
		}
		kcp.cwnd = 1
	}

	if kcp.cwnd < 1 {
		kcp.cwnd = 1
	}
}

// WaitSnd returns the number of segments waiting to be sent.
func (kcp *KCPConn) WaitSnd() int {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	return len(kcp.sndBuf) + len(kcp.sndQueue)
}

// RcvNxt returns the next expected receive sequence number.
func (kcp *KCPConn) RcvNxt() uint32 {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	return kcp.rcvNxt
}

// SndNxt returns the next send sequence number.
func (kcp *KCPConn) SndNxt() uint32 {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	return kcp.sndNxt
}

// KCPStats holds internal KCP state for diagnostics.
type KCPStats struct {
	RcvNxt      uint32
	RcvQueueLen int
	RcvBufLen   int
	AckListLen  int
	SndUna      uint32
	SndNxt      uint32
	SndBufLen   int
	SndQueueLen int
	RmtWnd      uint32
	CWnd        uint32
	RcvWnd      uint32
	Retransmits uint32
	SNDropOld   uint32
	SNDropAhead uint32
	LastDropSN  uint32
}

// Stats returns a snapshot of internal KCP state for diagnostics.
func (kcp *KCPConn) Stats() KCPStats {
	kcp.mu.Lock()
	defer kcp.mu.Unlock()
	return KCPStats{
		RcvNxt:      kcp.rcvNxt,
		RcvQueueLen: len(kcp.rcvQueue),
		RcvBufLen:   len(kcp.rcvBuf),
		AckListLen:  len(kcp.ackList) / 2, // pairs of (sn, ts)
		SndUna:      kcp.sndUna,
		SndNxt:      kcp.sndNxt,
		SndBufLen:   len(kcp.sndBuf),
		SndQueueLen: len(kcp.sndQueue),
		RmtWnd:      kcp.rmtWnd,
		CWnd:        kcp.cwnd,
		RcvWnd:      kcp.rcvWnd,
		Retransmits: kcp.retransmits,
		SNDropOld:   kcp.snDropOld,
		SNDropAhead: kcp.snDropAhead,
		LastDropSN:  kcp.lastDropSN,
	}
}

// updateRTO updates RTT estimation.
func (kcp *KCPConn) updateRTO(rtt int32) {
	if kcp.rxSrtt == 0 {
		kcp.rxSrtt = rtt
		kcp.rxRttval = rtt / 2
	} else {
		delta := rtt - kcp.rxSrtt
		if delta < 0 {
			delta = -delta
		}
		kcp.rxRttval = (3*kcp.rxRttval + delta) / 4
		kcp.rxSrtt = (7*kcp.rxSrtt + rtt) / 8
		if kcp.rxSrtt < 1 {
			kcp.rxSrtt = 1
		}
	}
	rto := kcp.rxSrtt + max32i(int32(kcp.interval), 4*kcp.rxRttval)
	kcp.rxRto = ibound(int32(kcp.rxMinRto), rto, IKCP_RTO_MAX)
}

// Utility functions
func itimediff(later, earlier uint32) int32 {
	return int32(later - earlier)
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func max32i(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func ibound(lower, mid, upper int32) int32 {
	if mid < lower {
		return lower
	}
	if mid > upper {
		return upper
	}
	return mid
}
