package gwell

import (
	"encoding/binary"
	"math/rand"
)

// GUTES Frame Header (decoded from live PCAP traffic analysis):
//
//  Offset  Size  Field
//  0       1     proto version (0x7F=plaintext, 0x7E=encrypted)
//  1       1     sub_type (frame type identifier)
//  2       2     frame_len (uint16 LE, total frame size including header)
//  4       4     word1 (session-specific, varies per frame)
//  8       4     word2 (session-specific, varies per frame)
//  12      4     sqnum (sequence number / session-specific)
//  16      4     chkval (checksum)
//  20      4     flags (opt_flags, varies by frame type)
//  24+     N     payload (variable)
//
// Frame sub-types (byte 1) observed in traffic:
//  0x15 = ListFrmRequest  (40 bytes,  client -> list server:51701)
//  0x16 = ListFrmResponse (176 bytes, list server -> client)
//  0x17 = DetectRequest   (44 bytes,  client -> P2P server)
//  0x01 = DetectRequest2  (68 bytes,  client -> P2P server, initial)
//  0x02 = DetectResponse  (56 bytes,  P2P server -> client)
//  0x0c = SessionInit     (164/48B,   bidirectional)
//  0x0d = SessionResp     (72/32B,    bidirectional)
//
// Encrypted frame sub-types (proto=0x7E):
//  0xAA = Certify (722-745B, DTLS certificates)
//  0xB0 = AvStreamCtl (108B)
//  0xB1 = Stream data (56B)
//  0xC3 = Detect (68B, encrypted variant)
//  0xA3 = ONLINE_MSG (186B)
//  0xA4 = CALLING (128B)
//
// Checksum algorithm (from iv_gute_frm_init_chkval ARM64 disassembly):
//   Verified correct against DetectResponse frames in live traffic.
//   chk = uint32(frame[20:24]) & 0xFFFFFF  // opt_flags masked
//   chk ^= uint32(frame[0:4])
//   chk ^= uint32(frame[4:8])
//   chk ^= uint32(frame[8:12])
//   chk ^= uint32(frame[12:16])
//   for i := 24; i < totalLen; i += 4 {
//       chk ^= uint32(frame[i:i+4])
//   }
//   frame[16:20] = chk

// opt_flags bit positions (from iv_gute_frm_rc5_encrypt disassembly)
const (
	FlagOptEncryptShift = 16 // bits 16:17 = encryption mode (0=none, 1=RC5-8B, 2=RC5-16B, 3=certify)
	FlagHasSignature    = 1 << 22 // bit 22: frame has 0x50 (80) byte signature appended
	FlagHasNTPTime      = 1 << 24 // bit 24: frame has 0x10 (16) byte NTP time appended
)

// GetEncryptDataLen computes how many bytes of payload are encrypted/checksummed.
// From gute_get_encrypt_data_len (at 0x2e774 in libiotp2pav.so):
//
//	encrypt_data_len = total_len - 0x18 (header)
//	if has_signature:  encrypt_data_len -= 0x50 (80 bytes)
//	if has_ntp_time:   encrypt_data_len -= 0x10 (16 bytes)
//
// Only this many bytes of payload participate in checksum and RC5 encryption.
// The remaining bytes (NTP time + signature) are plaintext.
func GetEncryptDataLen(frame []byte) int {
	if len(frame) < 24 {
		return 0
	}
	totalLen := int(binary.LittleEndian.Uint16(frame[2:4]))
	flags := binary.LittleEndian.Uint32(frame[20:24])

	encLen := totalLen - FrameHeaderSize
	if flags&FlagHasSignature != 0 {
		encLen -= 0x50
	}
	if flags&FlagHasNTPTime != 0 {
		encLen -= 0x10
	}
	if encLen < 0 {
		encLen = 0
	}
	return encLen
}

// InitChkval computes the GUTES frame checksum and stores it at offset 16.
// Only XORs payload words within the encrypted data region (respects flags).
func InitChkval(frame []byte) {
	if len(frame) < 24 {
		return
	}

	encLen := GetEncryptDataLen(frame)

	chk := binary.LittleEndian.Uint32(frame[20:24]) & 0xFFFFFF
	chk ^= binary.LittleEndian.Uint32(frame[0:4])
	chk ^= binary.LittleEndian.Uint32(frame[4:8])
	chk ^= binary.LittleEndian.Uint32(frame[8:12])
	chk ^= binary.LittleEndian.Uint32(frame[12:16])

	end := 24 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for i := 24; i+4 <= end; i += 4 {
		chk ^= binary.LittleEndian.Uint32(frame[i : i+4])
	}

	binary.LittleEndian.PutUint32(frame[16:20], chk)
}

// VerifyChkval checks if a frame's checksum is valid.
// Only XORs payload words within the encrypted data region (respects flags).
func VerifyChkval(frame []byte) bool {
	if len(frame) < 24 {
		return false
	}
	stored := binary.LittleEndian.Uint32(frame[16:20])

	encLen := GetEncryptDataLen(frame)

	chk := binary.LittleEndian.Uint32(frame[20:24]) & 0xFFFFFF
	chk ^= binary.LittleEndian.Uint32(frame[0:4])
	chk ^= binary.LittleEndian.Uint32(frame[4:8])
	chk ^= binary.LittleEndian.Uint32(frame[8:12])
	chk ^= binary.LittleEndian.Uint32(frame[12:16])

	end := 24 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for i := 24; i+4 <= end; i += 4 {
		chk ^= binary.LittleEndian.Uint32(frame[i : i+4])
	}

	return chk == stored
}

// Protocol constants (corrected from PCAP analysis)
const (
	// Protocol version bytes
	ProtoPlaintext byte = 0x7F           // Unencrypted control frames
	ProtoEncrypted byte = 0x7E           // Encrypted session frames
	ProtoVersion        = ProtoPlaintext // Default for outgoing control frames

	// Frame sub-type identifiers (byte 1)
	SubTypeListRequest   byte = 0x15 // ListFrmRequest
	SubTypeListResponse  byte = 0x16 // ListFrmResponse
	SubTypeDetectRequest byte = 0x17 // DetectRequest (to discovered servers)
	SubTypeDetectReq2    byte = 0x01 // DetectRequest2 (initial detect)
	SubTypeDetectResp    byte = 0x02 // DetectResponse
	SubTypeSessionInit   byte = 0x0C // Session initialization / CertifyReq
	SubTypeSessionResp   byte = 0x0D // Session response
	SubTypeHeartbeat     byte = 0xA0 // Heartbeat (keepalive)
	SubTypeInitInfoMsg   byte = 0xA6 // Mars init_info_msg (registration)
	SubTypeSubscribe     byte = 0xB0 // Subscribe device
	SubTypeSubscribeResp byte = 0xB1 // Subscribe response (device status)
	SubTypeCalling       byte = 0xA4 // CALLING (connect to device)
	SubTypeNetworkDetect byte = 0xB9 // P2P inner message (network detect)

	// Frame header size (bytes before payload)
	FrameHeaderSize = 24

	// Frame total sizes (from PCAP)
	ListRequestSize  = 40
	ListResponseSize = 176
	DetectReqSize    = 44
	DetectReq2Size   = 68
	DetectRespSize   = 56
	SessionInitSize  = 164
)

// BuildListFrmRequest constructs a 40-byte ListFrmRequest frame.
// Layout from PCAP: proto=0x7F, sub=0x15, len=0x0028(40)
func BuildListFrmRequest() []byte {
	buf := make([]byte, ListRequestSize)
	buf[0] = ProtoPlaintext
	buf[1] = SubTypeListRequest
	binary.LittleEndian.PutUint16(buf[2:4], ListRequestSize)

	// Words 4-15: session-specific random data (observed as random in PCAP)
	binary.LittleEndian.PutUint32(buf[4:8], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[8:12], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[12:16], rand.Uint32())

	// Flags at 20-23: 0x00010000 observed in traffic
	binary.LittleEndian.PutUint32(buf[20:24], 0x00010000)

	// Payload (24-39): 16 bytes of random session data
	binary.LittleEndian.PutUint32(buf[24:28], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[28:32], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[32:36], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[36:40], rand.Uint32())

	InitChkval(buf)
	return buf
}

// BuildDetectReq2 constructs a 68-byte initial DetectRequest2 frame.
// Layout from PCAP: proto=0x7F, sub=0x01, len=0x0044(68)
func BuildDetectReq2() []byte {
	buf := make([]byte, DetectReq2Size)
	buf[0] = ProtoPlaintext
	buf[1] = SubTypeDetectReq2
	binary.LittleEndian.PutUint16(buf[2:4], DetectReq2Size)

	// Words 4-15: session-specific data
	binary.LittleEndian.PutUint32(buf[4:8], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[8:12], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[12:16], rand.Uint32())

	// Flags at 20-23
	binary.LittleEndian.PutUint32(buf[20:24], 0x00010000)

	// Payload: 44 bytes (68 - 24 header)
	// From PCAP, the payload contains 8 bytes of device data followed by
	// repeated 8-byte blocks (observed as identical 8-byte values)
	for i := 24; i < DetectReq2Size; i += 4 {
		binary.LittleEndian.PutUint32(buf[i:i+4], rand.Uint32())
	}

	// Last 4 bytes are zeros
	binary.LittleEndian.PutUint32(buf[64:68], 0)

	InitChkval(buf)
	return buf
}

// BuildDetectRequest constructs a 44-byte DetectRequest frame.
// Layout from PCAP: proto=0x7F, sub=0x17, len=0x002C(44)
// Sent to discovered P2P servers after list response.
func BuildDetectRequest() []byte {
	buf := make([]byte, DetectReqSize)
	buf[0] = ProtoPlaintext
	buf[1] = SubTypeDetectRequest
	binary.LittleEndian.PutUint16(buf[2:4], DetectReqSize)

	// Words 4-15: session data
	binary.LittleEndian.PutUint32(buf[4:8], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[8:12], rand.Uint32())
	binary.LittleEndian.PutUint32(buf[12:16], rand.Uint32())

	// Flags
	binary.LittleEndian.PutUint32(buf[20:24], 0x00010000)

	// Payload: 20 bytes
	for i := 24; i < DetectReqSize; i += 4 {
		binary.LittleEndian.PutUint32(buf[i:i+4], rand.Uint32())
	}

	// Last 4 bytes are zeros
	binary.LittleEndian.PutUint32(buf[40:44], 0)

	InitChkval(buf)
	return buf
}
