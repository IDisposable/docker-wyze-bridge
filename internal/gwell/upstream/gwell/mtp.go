package gwell

// MTP (Media Transport Protocol) frame building, KCP segment helpers,
// and session control message builders for the GWell P2P camera protocol.
// All functions are stateless — they build/parse frames from parameters.

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

// MTPPayloadOffset returns the byte offset where KCP/payload data starts in an MTP frame.
// Standard header = 6 bytes. Extended header (relay) = 14 bytes.
func MTPPayloadOffset(flags byte) int {
	off := 6
	if (flags>>5)&3 != 0 {
		off = 14 // extended header: [6:14] = relay routing TID (8 bytes)
	}
	return off
}

// MTPChecksum computes the MTP frame checksum.
// SDK: iv_mtpfrm_calc_chval starts at frame+6, processes 12 uint16 values.
func MTPChecksum(frame []byte) uint16 {
	var cs uint16
	for i := 0; i < 12; i++ {
		off := 6 + i*2
		if off+2 > len(frame) {
			break
		}
		val := binary.LittleEndian.Uint16(frame[off : off+2])
		rotated := (val << uint(i)) | (val >> uint(16-i))
		cs ^= rotated
	}
	return cs
}

// BuildMTPFrame wraps payload in a 6-byte MTP header (0xC0 magic).
func BuildMTPFrame(payload []byte, urgent bool) []byte {
	totalLen := len(payload) + 6
	frame := make([]byte, totalLen)
	frame[0] = 0xC0
	flags := byte(0x10)
	if urgent {
		flags |= 0x80
	}
	frame[1] = flags
	frame[2] = byte(totalLen & 7)
	frame[3] = byte((totalLen >> 3) & 0xFF)
	copy(frame[6:], payload)
	cs := MTPChecksum(frame)
	lenField := binary.LittleEndian.Uint16(frame[2:4])
	binary.LittleEndian.PutUint16(frame[4:6], cs^lenField)
	return frame
}

// BuildExtendedMTPFrame wraps payload in a 14-byte extended MTP header with TID.
// Used for UDP relay (extended header with relay SessionID).
func BuildExtendedMTPFrame(payload []byte, destTID uint64, urgent bool) []byte {
	totalLen := len(payload) + 14
	frame := make([]byte, totalLen)
	frame[0] = 0xC0
	flags := byte(1 << 5) // mode_bits=1 in bits 5-6
	if urgent {
		flags |= 0x80
	}
	frame[1] = flags
	frame[2] = byte(totalLen & 7)
	frame[3] = byte((totalLen >> 3) & 0xFF)
	binary.LittleEndian.PutUint64(frame[6:14], destTID)
	copy(frame[14:], payload)
	cs := MTPChecksum(frame)
	lenField := binary.LittleEndian.Uint16(frame[2:4])
	binary.LittleEndian.PutUint16(frame[4:6], cs^lenField)
	return frame
}

// BuildCreateKCPSessionMsg builds the 28-byte "create KCP session" message (0x25 command).
func BuildCreateKCPSessionMsg(linkID uint32, callingID uint64, calledID uint64) []byte {
	msg := make([]byte, 28)
	msg[0] = 0x00
	msg[1] = 0x25
	binary.LittleEndian.PutUint16(msg[2:4], 0x001C) // length = 28
	binary.LittleEndian.PutUint32(msg[4:8], linkID)
	binary.LittleEndian.PutUint64(msg[12:20], callingID)
	binary.LittleEndian.PutUint64(msg[20:28], calledID)
	return msg
}

// BuildMeterAckFromRequest builds a meter ACK (cmd=0x02) by echoing the incoming
// meter request (cmd=0x01) with src_id/dst_id swapped.
func BuildMeterAckFromRequest(reqPayload []byte) []byte {
	if len(reqPayload) < 68 {
		padded := make([]byte, 68)
		copy(padded, reqPayload)
		reqPayload = padded
	}
	ack := make([]byte, 68)
	copy(ack, reqPayload[:68])
	ack[1] = 0x02
	var tmp [8]byte
	copy(tmp[:], ack[12:20])
	copy(ack[12:20], ack[20:28])
	copy(ack[20:28], tmp[:])
	return ack
}

// BuildMeterProbe builds a meter PROBE (cmd=0x01) initiated by us.
// SDK: iv_mtpSession_send_meter_proc sends these every 2s to keep the session alive.
// The camera kills the session if no meter probes arrive for ~119 seconds.
func BuildMeterProbe(linkID uint32, srcID uint64, dstID uint64, round uint32) []byte {
	probe := make([]byte, 68)
	probe[0] = 0x00
	probe[1] = 0x01 // meter request/probe
	binary.LittleEndian.PutUint16(probe[2:4], 0x0044) // length = 68
	binary.LittleEndian.PutUint32(probe[4:8], linkID)
	binary.LittleEndian.PutUint64(probe[12:20], srcID)
	binary.LittleEndian.PutUint64(probe[20:28], dstID)
	binary.LittleEndian.PutUint32(probe[28:32], round)
	binary.LittleEndian.PutUint64(probe[36:44], uint64(time.Now().UnixMilli()))
	return probe
}

// BuildKCPPushSegment builds a KCP push (cmd=81) segment with the given data.
func BuildKCPPushSegment(conv uint32, sn uint32, ts uint32, data []byte) []byte {
	seg := make([]byte, 24+len(data))
	binary.LittleEndian.PutUint32(seg[0:4], conv)
	seg[4] = 81 // push
	seg[5] = 0  // frg
	binary.LittleEndian.PutUint16(seg[6:8], 128)
	binary.LittleEndian.PutUint32(seg[8:12], ts)
	binary.LittleEndian.PutUint32(seg[12:16], sn)
	binary.LittleEndian.PutUint32(seg[16:20], 0) // una
	binary.LittleEndian.PutUint32(seg[20:24], uint32(len(data)))
	copy(seg[24:], data)
	return seg
}

// BuildKCPAckSegment builds a KCP ACK (cmd=82) segment.
func BuildKCPAckSegment(conv uint32, sn uint32, ts uint32, una uint32) []byte {
	seg := make([]byte, 24)
	binary.LittleEndian.PutUint32(seg[0:4], conv)
	seg[4] = 82 // ack
	seg[5] = 0
	binary.LittleEndian.PutUint16(seg[6:8], 128)
	binary.LittleEndian.PutUint32(seg[8:12], ts)
	binary.LittleEndian.PutUint32(seg[12:16], sn)
	binary.LittleEndian.PutUint32(seg[16:20], una)
	binary.LittleEndian.PutUint32(seg[20:24], 0)
	return seg
}

// BuildAVStreamCtlINITREQ builds a 76-byte AVSTREAMCTL INITREQ (cmd=1).
func BuildAVStreamCtlINITREQ(sessionID uint32, connType uint32, callAction uint32, channelField uint16, avKey []byte) []byte {
	buf := make([]byte, 76)
	buf[0] = 3
	buf[1] = 0x02
	binary.LittleEndian.PutUint16(buf[2:4], 0x004C)
	binary.LittleEndian.PutUint32(buf[4:8], sessionID)
	binary.LittleEndian.PutUint32(buf[8:12], 1)  // cmd = INITREQ
	binary.LittleEndian.PutUint32(buf[12:16], 0)  // reason
	binary.LittleEndian.PutUint32(buf[16:20], connType)
	binary.LittleEndian.PutUint32(buf[20:24], callAction)
	if len(avKey) >= 32 {
		copy(buf[24:56], avKey[:32])
	}
	binary.LittleEndian.PutUint16(buf[62:64], channelField)
	return buf
}

// BuildAVStreamCtlSTART builds a 76-byte AVSTREAMCTL START (cmd=6).
func BuildAVStreamCtlSTART(sessionID uint32) []byte {
	buf := make([]byte, 76)
	buf[0] = 3
	binary.LittleEndian.PutUint16(buf[2:4], 0x004C)
	binary.LittleEndian.PutUint32(buf[4:8], sessionID)
	binary.LittleEndian.PutUint32(buf[8:12], 6)
	return buf
}

// BuildTCPRelayRegister builds the 74-byte TCP relay registration frame.
func BuildTCPRelayRegister(linkID uint32, callingID uint64, calledID uint64) []byte {
	frame := make([]byte, 74)
	frame[0] = 0xC0
	frame[1] = 0x80
	frame[2] = byte(74 & 7)
	frame[3] = byte((74 >> 3) & 0xFF)
	frame[6] = 0x00
	frame[7] = 0x01
	binary.LittleEndian.PutUint16(frame[8:10], 0x0044)
	binary.LittleEndian.PutUint32(frame[10:14], linkID)
	binary.LittleEndian.PutUint64(frame[18:26], callingID)
	binary.LittleEndian.PutUint64(frame[26:34], calledID)
	ts := uint64(time.Now().UnixMilli())
	binary.LittleEndian.PutUint64(frame[38:46], ts)
	cs := MTPChecksum(frame)
	lenField := binary.LittleEndian.Uint16(frame[2:4])
	binary.LittleEndian.PutUint16(frame[4:6], cs^lenField)
	return frame
}

// BuildMTPResRequest builds a GUTES-wrapped MTP_RES_REQUEST (sub=0xA2).
func BuildMTPResRequest(token *AccessToken, linkID uint32, calledTID uint64,
	routingSessionID uint64, sessionKey, pwdKey *RC5Key) []byte {
	frameLen := 0x7A // 122 bytes
	frame := make([]byte, frameLen)
	frame[0] = 0x7E
	frame[1] = 0xA2
	binary.LittleEndian.PutUint16(frame[2:4], uint16(frameLen))
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[12:16], 0)
	flags := uint32(2<<16) | uint32(1<<18)
	binary.LittleEndian.PutUint32(frame[20:24], flags)
	binary.LittleEndian.PutUint16(frame[0x18:0x1A], 0)
	binary.LittleEndian.PutUint32(frame[0x1A:0x1E], linkID)
	binary.LittleEndian.PutUint64(frame[0x1E:0x26], token.AccessID)
	binary.LittleEndian.PutUint64(frame[0x26:0x2E], calledTID)
	InitChkval(frame)
	EncryptFrameMode2(frame, sessionKey, pwdKey)
	return frame
}

// BuildPortStatReq builds a PortStatReq (sub=0xCA) frame.
func BuildPortStatReq(token *AccessToken, routingSessionID uint64, sqnum uint32,
	linkID uint32, calledTID uint64, pwdKey *RC5Key) []byte {
	frame := make([]byte, 0x34)
	frame[0] = 0x7F
	frame[1] = 0xCA
	binary.LittleEndian.PutUint16(frame[2:4], 0x34)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[20:24], uint32(1<<16))
	binary.LittleEndian.PutUint64(frame[28:36], calledTID)
	binary.LittleEndian.PutUint32(frame[36:40], linkID)
	frame[41] = 1
	InitChkval(frame)
	EncryptFrameFull(frame, pwdKey)
	return frame
}

// BuildPortStatResp builds a PortStatResp (sub=0xCB) frame.
func BuildPortStatResp(token *AccessToken, routingSessionID uint64, sqnum uint32,
	linkID uint32, calledTID uint64, pwdKey *RC5Key) []byte {
	frame := make([]byte, 0x34)
	frame[0] = 0x7F
	frame[1] = 0xCB
	binary.LittleEndian.PutUint16(frame[2:4], 0x34)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[20:24], uint32(1<<16))
	binary.LittleEndian.PutUint64(frame[28:36], calledTID)
	binary.LittleEndian.PutUint32(frame[36:40], linkID)
	frame[41] = 1
	InitChkval(frame)
	EncryptFrameFull(frame, pwdKey)
	return frame
}

// BuildSessionSocket builds a GUTES 0xCA frame for relay port activation.
func BuildSessionSocket(token *AccessToken, routingSessionID uint64, sqnum uint32,
	linkID uint32, calledTID uint64, subType byte, relayPorts []uint16,
	pwdKey *RC5Key) []byte {
	frame := make([]byte, 0x34)
	frame[0] = 0x7F
	frame[1] = 0xCA
	binary.LittleEndian.PutUint16(frame[2:4], 0x34)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[20:24], uint32(1<<16))
	binary.LittleEndian.PutUint64(frame[28:36], calledTID)
	binary.LittleEndian.PutUint32(frame[36:40], linkID)
	frame[40] = 1
	frame[41] = subType
	for i := 0; i < 4 && i < len(relayPorts); i++ {
		binary.LittleEndian.PutUint16(frame[44+i*2:46+i*2], relayPorts[i])
	}
	InitChkval(frame)
	EncryptFrameFull(frame, pwdKey)
	return frame
}

// BuildPassthroughControl builds a GUTES PASSTHROUGH_MSG (sub=0xB9) CONTROL frame.
func BuildPassthroughControl(token *AccessToken, routingSessionID uint64, sqnum uint32,
	targetTID uint64, command byte, linkID uint32, sessionKey, pwdKey *RC5Key) []byte {
	const totalSize = 76
	frame := make([]byte, totalSize)
	frame[0] = 0x7E
	frame[1] = 0xB9
	binary.LittleEndian.PutUint16(frame[2:4], totalSize)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)
	binary.LittleEndian.PutUint32(frame[20:24], uint32(2<<16)|uint32(1<<18))
	binary.LittleEndian.PutUint32(frame[24:28], 3)
	binary.LittleEndian.PutUint64(frame[28:36], targetTID)
	binary.LittleEndian.PutUint64(frame[36:44], token.AccessID)
	frame[52] = command
	binary.LittleEndian.PutUint32(frame[56:60], linkID)
	EncryptFrameMode2(frame, sessionKey, pwdKey)
	return frame
}

// BuildPassthroughData builds a GUTES PASSTHROUGH_MSG (sub=0xB9) DATA frame.
func BuildPassthroughData(token *AccessToken, routingSessionID uint64, sqnum uint32,
	targetTID uint64, msgID uint32, payload []byte, sessionKey, pwdKey *RC5Key) []byte {
	totalSize := 52 + len(payload)
	frame := make([]byte, totalSize)
	frame[0] = 0x7E
	frame[1] = 0xB9
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
	binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)
	binary.LittleEndian.PutUint32(frame[20:24], uint32(2<<16)|uint32(1<<18))
	binary.LittleEndian.PutUint32(frame[24:28], 0)
	binary.LittleEndian.PutUint64(frame[28:36], targetTID)
	binary.LittleEndian.PutUint64(frame[36:44], token.AccessID)
	binary.LittleEndian.PutUint32(frame[44:48], msgID)
	binary.LittleEndian.PutUint16(frame[48:50], uint16(len(payload)))
	copy(frame[52:], payload)
	EncryptFrameMode2(frame, sessionKey, pwdKey)
	return frame
}

// TryDecrypt attempts to decrypt a GUTES frame using mode 2, mode 1, and mode 0.
// Returns decrypted frame and mode name, or nil if all fail.
func TryDecrypt(raw []byte, n int, sessionKey, pwdKey *RC5Key) ([]byte, string) {
	if n < 24 {
		return nil, ""
	}
	flags := binary.LittleEndian.Uint32(raw[20:24])
	encMode := (flags >> 16) & 3
	trial := make([]byte, n)

	switch encMode {
	case 2:
		copy(trial, raw[:n])
		if DecryptFrameMode2(trial, sessionKey, pwdKey) {
			return trial, "mode-2"
		}
		copy(trial, raw[:n])
		if DecryptFrameFull(trial, pwdKey) {
			return trial, "mode-1"
		}
	case 1:
		copy(trial, raw[:n])
		if DecryptFrameFull(trial, pwdKey) {
			return trial, "mode-1"
		}
		copy(trial, raw[:n])
		if DecryptFrameMode2(trial, sessionKey, pwdKey) {
			return trial, "mode-2"
		}
	case 0:
		copy(trial, raw[:n])
		DecryptID(trial, pwdKey)
		if VerifyChkval(trial) {
			return trial, "mode-0"
		}
	}
	// Fallback
	copy(trial, raw[:n])
	if DecryptFrameFull(trial, pwdKey) {
		return trial, "mode-1(fallback)"
	}
	copy(trial, raw[:n])
	if DecryptFrameMode2(trial, sessionKey, pwdKey) {
		return trial, "mode-2(fallback)"
	}
	copy(trial, raw[:n])
	DecryptID(trial, pwdKey)
	if VerifyChkval(trial) {
		return trial, "mode-0(fallback)"
	}
	return nil, ""
}

// FeedMTPToKCP extracts KCP data from an MTP frame and feeds it to the appropriate
// KCP instance. Returns session control command (0x10000|cmd) if found, 0 otherwise.
func FeedMTPToKCP(buf []byte, n int, dataKCP, ctrlKCP *KCPConn, convData, convCtrl uint32) uint32 {
	if n < 6 || buf[0] != 0xC0 {
		return 0
	}
	flags := buf[1]
	payOff := MTPPayloadOffset(flags)
	if n < payOff+4 {
		return 0
	}
	sessCmd := uint32(0)
	if n >= payOff+8 && buf[payOff] == 0x00 {
		cmd := buf[payOff+1]
		isCtrl := cmd == 0x01 || cmd == 0x02 || cmd == 0x04 || cmd == 0x06 ||
			cmd == 0x21 || cmd == 0x24 || cmd == 0x25
		if isCtrl {
			sessLen := binary.LittleEndian.Uint16(buf[payOff+2 : payOff+4])
			sessCmd = 0x10000 | uint32(cmd)
			payOff += int(sessLen)
		}
	}
	if payOff+24 <= n {
		kcpData := buf[payOff:n]
		firstConv := binary.LittleEndian.Uint32(kcpData[0:4])
		allSameConv := true
		off := 0
		for off+24 <= len(kcpData) {
			segConv := binary.LittleEndian.Uint32(kcpData[off : off+4])
			segLen := binary.LittleEndian.Uint32(kcpData[off+20 : off+24])
			if segConv != firstConv {
				allSameConv = false
				break
			}
			off += 24 + int(segLen)
		}
		if allSameConv {
			if firstConv == convCtrl && ctrlKCP != nil {
				ctrlKCP.Input(kcpData)
			} else if firstConv == convData && dataKCP != nil {
				dataKCP.Input(kcpData)
			}
		} else {
			off = 0
			for off+24 <= len(kcpData) {
				segConv := binary.LittleEndian.Uint32(kcpData[off : off+4])
				segLen := binary.LittleEndian.Uint32(kcpData[off+20 : off+24])
				segEnd := off + 24 + int(segLen)
				if segEnd > len(kcpData) {
					break
				}
				seg := kcpData[off:segEnd]
				if segConv == convCtrl && ctrlKCP != nil {
					ctrlKCP.Input(seg)
				} else if segConv == convData && dataKCP != nil {
					dataKCP.Input(seg)
				}
				off = segEnd
			}
		}
	}
	return sessCmd
}

// DecryptMTPPayload decrypts an MTP session TLV frame received via KCP.
// Format: [type(1)][flags(1)][total_len(2, LE)][RC5-encrypted payload...]
// Returns decoded H.264 data for type=0x04 AV frames, or nil.
func DecryptMTPPayload(data []byte, rc5Key *RC5Key, channel string) []byte {
	if len(data) < 4 {
		return nil
	}
	frameType := data[0]
	totalLen := int(binary.LittleEndian.Uint16(data[2:4]))

	if frameType == 0x03 {
		return nil // AVSTREAMCTL not encrypted, no H.264
	}
	if frameType != 0x02 && frameType != 0x04 {
		return nil
	}
	if rc5Key == nil {
		return nil
	}

	payloadLen := totalLen - 4
	if payloadLen < 0 || totalLen > len(data) {
		payloadLen = len(data) - 4
	}
	numBlocks := payloadLen / 8
	for i := 0; i < numBlocks; i++ {
		off := 4 + i*8
		rc5Key.DecryptBlock(data[off : off+8])
	}

	// Extract H.264 from AV data
	if frameType == 0x04 && channel == "DATA" {
		avPayload := data[4:]
		if totalLen > 4 && totalLen <= len(data) {
			avPayload = data[4:totalLen]
		}
		if len(avPayload) >= 28 && avPayload[0] == 0xFF && avPayload[1] == 0xFF &&
			avPayload[2] == 0xFF && avPayload[3] == 0x88 {
			h264Data := avPayload[28:]
			if len(h264Data) > 0 {
				return h264Data
			}
		} else if len(avPayload) > 0 {
			return avPayload
		}
	}
	return nil
}

// ParseMTPForAVSTREAMCTL parses an MTP frame for AVSTREAMCTL commands.
// Returns: 2=ACCEPT, 6=START, 1=INITREQ, 0x10001=meter_req, or 0.
// If ACCEPT found and acceptKeyOut is non-nil, writes the 32-byte key.
func ParseMTPForAVSTREAMCTL(buf []byte, n int, convCtrl, convData uint32, acceptKeyOut *[]byte) uint32 {
	if n < 6 || buf[0] != 0xC0 {
		return 0
	}
	flags := buf[1]
	payOff := MTPPayloadOffset(flags)
	if n < payOff+4 {
		return 0
	}

	sessionCtrlCmd := uint32(0)
	if n >= payOff+8 && buf[payOff] == 0x00 {
		sessCmd := buf[payOff+1]
		isSessionCtrl := sessCmd == 0x01 || sessCmd == 0x02 || sessCmd == 0x04 || sessCmd == 0x06 ||
			sessCmd == 0x21 || sessCmd == 0x24 || sessCmd == 0x25
		if isSessionCtrl {
			sessLen := binary.LittleEndian.Uint16(buf[payOff+2 : payOff+4])
			sessionCtrlCmd = 0x10000 | uint32(sessCmd)
			payOff += int(sessLen)
			if payOff >= n {
				return sessionCtrlCmd
			}
		}
	}

	bestAvCmd := uint32(0)
	kcpStart := payOff
	for kcpStart+24 <= n {
		kcpCmd := buf[kcpStart+4]
		kcpLen := binary.LittleEndian.Uint32(buf[kcpStart+20 : kcpStart+24])

		kcpDataStart := kcpStart + 24
		if kcpCmd == 81 && kcpLen > 0 && n >= kcpDataStart+int(kcpLen) {
			kcpData := buf[kcpDataStart : kcpDataStart+int(kcpLen)]
			if kcpLen >= 12 && kcpData[0] == 3 {
				avCmd := binary.LittleEndian.Uint32(kcpData[8:12])
				if avCmd == 2 && kcpLen >= 56 && acceptKeyOut != nil {
					key := make([]byte, 32)
					copy(key, kcpData[24:56])
					*acceptKeyOut = key
				}
				if avCmd == 2 || bestAvCmd == 0 {
					bestAvCmd = avCmd
				}
			}
		}
		kcpStart += 24 + int(kcpLen)
		if kcpLen > 65536 {
			break
		}
	}

	if sessionCtrlCmd != 0 && bestAvCmd == 0 {
		return sessionCtrlCmd
	}
	if bestAvCmd != 0 {
		return bestAvCmd
	}
	return sessionCtrlCmd
}

// GetOutboundIP returns our local IP that routes to the given target IP.
func GetOutboundIP(targetIP string) net.IP {
	conn, err := net.Dial("udp4", targetIP+":1")
	if err != nil {
		return nil
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}

// ReadGUTEFrame reads a single GUTE frame from a TCP connection.
func ReadGUTEFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	_, err := io.ReadFull(conn, header)
	if err != nil {
		return nil, err
	}
	frameLen := int(binary.LittleEndian.Uint16(header[2:4]))
	if frameLen < 4 || frameLen > 65536 {
		return header, fmt.Errorf("invalid GUTE frame length %d", frameLen)
	}
	frame := make([]byte, frameLen)
	copy(frame[:4], header)
	if frameLen > 4 {
		_, err = io.ReadFull(conn, frame[4:])
		if err != nil {
			return frame[:4], fmt.Errorf("incomplete GUTE frame: %w", err)
		}
	}
	return frame, nil
}

// ReadMTPFrameFromTCP reads a single MTP frame from a TCP connection.
func ReadMTPFrameFromTCP(conn net.Conn, deadline time.Time) ([]byte, error) {
	conn.SetReadDeadline(deadline)
	header := make([]byte, 4)
	_, err := io.ReadFull(conn, header)
	if err != nil {
		return nil, err
	}
	if header[0] != 0xC0 {
		return header, fmt.Errorf("unexpected magic 0x%02X", header[0])
	}
	totalLen := int(header[2]&7) | int(header[3])<<3
	if totalLen < 6 || totalLen > 65536 {
		return header, fmt.Errorf("invalid MTP length %d", totalLen)
	}
	frame := make([]byte, totalLen)
	copy(frame[:4], header)
	_, err = io.ReadFull(conn, frame[4:])
	if err != nil {
		return frame[:4], fmt.Errorf("incomplete MTP frame: %w", err)
	}
	return frame, nil
}

// RelayAddr holds a relay server address from MTP_RES_RESPONSE.
type RelayAddr struct {
	IP        net.IP
	Port      uint16
	TCP       bool
	SessionID uint64
}

// UDPRelayTarget is a resolved UDP relay target with its session ID.
type UDPRelayTarget struct {
	Addr      *net.UDPAddr
	SessionID uint64
}

// min returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// LogMTPStats logs KCP and session statistics.
func LogMTPStats(prefix string, dataKCP, ctrlKCP *KCPConn, streamPkts, streamDataBytes int) {
	ds := dataKCP.Stats()
	cs := ctrlKCP.Stats()
	log.Printf("%s STREAM: %d pkts, %d bytes", prefix, streamPkts, streamDataBytes)
	log.Printf("  DATA-KCP: rcvNxt=%d sndUna=%d sndNxt=%d rmtWnd=%d cwnd=%d",
		ds.RcvNxt, ds.SndUna, ds.SndNxt, ds.RmtWnd, ds.CWnd)
	log.Printf("  CTRL-KCP: rcvNxt=%d sndUna=%d sndNxt=%d",
		cs.RcvNxt, cs.SndUna, cs.SndNxt)
}
