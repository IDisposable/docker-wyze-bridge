package gwell

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"strconv"
	"time"
)

// AccessToken holds parsed access credentials from the Wyze API.
// The accessToken hex string is decoded into binary, and the RC5 key
// for CertifyReq inner encryption is extracted from bytes [48:63].
//
// From iv_set_access_token @ 0x12273c:
//
//	hex_str_to_bytes(token, buffer, 0x80)  → first 128 hex chars → 64 bytes
//	RC5 key = buffer[0x30:0x40] = decoded_token[48:64]
type AccessToken struct {
	// AccessID is the 64-bit access identifier, split into word1 (low 32) and word2 (high 32).
	AccessID uint64

	// TokenBytes is the full decoded token (64 bytes from first 128 hex chars).
	TokenBytes [64]byte

	// TokenKey is the 16-byte RC5-64 key for CertifyReq inner encryption.
	// Extracted from TokenBytes[48:64].
	TokenKey *RC5Key64

	// TokenKeyRaw is the raw 16 bytes for use in signature generation.
	TokenKeyRaw [16]byte

	// ExtraTokenData is the base64-decoded extra token data (chars after position 128).
	// Used for token-based subscribe (0xB0). Up to 80 bytes.
	ExtraTokenData []byte
}

// ParseAccessToken parses the accessId (string integer) and accessToken (hex string)
// from the Wyze API into a structured AccessToken.
func ParseAccessToken(accessID, accessToken string) (*AccessToken, error) {
	// Try unsigned first, fall back to signed (API returns signed int64)
	id, err := strconv.ParseUint(accessID, 10, 64)
	if err != nil {
		// The Wyze API returns accessId as a signed int64, but the protocol
		// uses it as uint64. Parse as signed and reinterpret the bits.
		signedID, err2 := strconv.ParseInt(accessID, 10, 64)
		if err2 != nil {
			return nil, fmt.Errorf("parse accessId %q: %w", accessID, err)
		}
		id = uint64(signedID)
	}

	if len(accessToken) < 128 {
		return nil, fmt.Errorf("accessToken too short: %d hex chars (need >= 128)", len(accessToken))
	}

	// Decode first 128 hex chars → 64 bytes
	decoded, err := hex.DecodeString(accessToken[:128])
	if err != nil {
		return nil, fmt.Errorf("decode accessToken hex: %w", err)
	}

	at := &AccessToken{
		AccessID: id,
	}
	copy(at.TokenBytes[:], decoded)
	copy(at.TokenKeyRaw[:], decoded[48:64])
	at.TokenKey = NewRC5Key64(at.TokenKeyRaw[:])

	// Extract extra token data (base64-encoded portion after first 128 hex chars).
	// SDK: iv_subscribe_dev skips first 0x80 chars, base64-decodes rest to get 0x50 bytes.
	if len(accessToken) > 128 {
		extra := accessToken[128:]
		extraBytes, err := base64.StdEncoding.DecodeString(extra)
		if err != nil {
			// Try RawStdEncoding (no padding)
			extraBytes, err = base64.RawStdEncoding.DecodeString(extra)
		}
		if err == nil && len(extraBytes) > 0 {
			at.ExtraTokenData = extraBytes
		}
	}

	return at, nil
}

// Word1 returns the low 32 bits of the AccessID.
func (at *AccessToken) Word1() uint32 {
	return uint32(at.AccessID)
}

// Word2 returns the high 32 bits of the AccessID.
func (at *AccessToken) Word2() uint32 {
	return uint32(at.AccessID >> 32)
}

// FrameRC5Key32 returns a 32-bit RC5 key from the token's key bytes
// (used for mode-1 EncryptID in the standard frame pipeline).
// From iv_set_access_token: rc5_ctx_setkey(ctx+0x130, token+0x32c, 0x10)
// The password key context at offset 0x130 is ALWAYS the "www.gwell.cc" key.
// Token key at 0x148 is separate for inner encryption only.
func (at *AccessToken) PasswordRC5Key() *RC5Key {
	return NewPasswordKey()
}

// CertifyResult holds the output of a successful CertifyReq/CertifyResp exchange.
type CertifyResult struct {
	SessionID  uint64   // 8-byte session ID from CertifyResp
	SessionKey *RC5Key  // 32-byte random key → RC5-32 key for session encryption
	RandomKey  [32]byte // raw 32-byte random key
}

// BuildCertifyReq constructs a CertifyReq frame (sub_type=0x0C).
//
// From iv_gutes_start_active_certify_req @ 0x134864:
//
//	[0]     proto = 0x7F
//	[1]     sub_type = 0x0C
//	[2:4]   frame_len (updated as optional fields are added)
//	[4:8]   word1 = accessID low 32 bits
//	[8:12]  word2 = accessID high 32 bits
//	[12:16] sqnum
//	[16:20] chkval (computed)
//	[20:24] flags = 0x01410000 (encrypt_mode=1 | has_signature | has_ntp_time)
//	--- Encrypted payload (starts at offset 24) ---
//	[24:28] opt_flags (bit 0 = has_version)
//	[28:32] hash = giot_hash_string(random_key, 32)
//	[32:64] random_key encrypted with RC5-64 (2 blocks of 16 bytes)
//	[64:68] version (4 bytes, if has_version) = MTU value
//	--- Non-encrypted trailing fields ---
//	[N:N+16]    NTP time (16 bytes)
//	[N+16:N+96] signature (80 bytes)
//
// Returns the frame bytes and the 32-byte random key for session setup.
func BuildCertifyReq(token *AccessToken, sqnum uint32) ([]byte, [32]byte) {
	// Base payload: opt_flags(4) + hash(4) + encrypted_key(32) = 40 bytes
	// + version(4) = 44 bytes encrypted payload
	// + NTP(16) + signature(80) = 96 bytes plaintext trailer
	// Total = 24 (header) + 44 (encrypted) + 16 (NTP) + 80 (sig) = 164 bytes
	const baseEncPayload = 44 // with version field
	const totalSize = FrameHeaderSize + baseEncPayload + 16 + 80

	frame := make([]byte, totalSize)
	frame[0] = ProtoPlaintext // 0x7F
	frame[1] = SubTypeSessionInit // 0x0C
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
	binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags from iv_gutes_add_send_pkt (PCAP: 0x014D9092):
	//   bits 1-15:  random value (set if bit 0 = 0)
	//   bits 16-17: encrypt_mode (1 = frame-derived RC5 key)
	//   bits 18-19: send_type (3 = has key + signature, from *param_3)
	//   bit 20:     cleared
	//   bit 22:     has_signature
	//   bit 24:     has_ntp_time
	randBits := uint32(mrand.Intn(0x7FFF)) << 1
	flags := randBits | (1 << 16) | (3 << 18) | FlagHasSignature | FlagHasNTPTime
	binary.LittleEndian.PutUint32(frame[20:24], flags)

	// --- Payload at offset 24 ---

	// opt_flags: bit 0 = has_version
	binary.LittleEndian.PutUint32(frame[24:28], 0x00000001)

	// Generate 32-byte random key
	var randomKey [32]byte
	rand.Read(randomKey[:])

	// Hash of the random key
	hash := GiotHashString(randomKey[:])
	binary.LittleEndian.PutUint32(frame[28:32], hash)

	// Copy random key into frame, then encrypt with RC5-64
	copy(frame[32:64], randomKey[:])
	token.TokenKey.EncryptBlock16(frame[32:48])  // block 1: bytes 0-15
	token.TokenKey.EncryptBlock16(frame[48:64])  // block 2: bytes 16-31

	// Version field (MTU = 1032 from PCAP, or 0x408)
	binary.LittleEndian.PutUint32(frame[64:68], 1032)

	// --- NTP time at offset 68 ---
	ntpTime := uint64(time.Now().UnixMilli())
	binary.LittleEndian.PutUint64(frame[68:76], ntpTime)
	// bytes 76-83 stay zero

	// The correct order from decompiled iv_gutes_add_send_pkt @ 0x131b64:
	// 1. Compute checksum (needed for HMAC in signature)
	// 2. Build signature (uses checksum value)
	// 3. Apply frame encryption

	// Step 1: Compute checksum FIRST (signature needs the chkval for HMAC)
	InitChkval(frame)

	// Step 2: Build signature at offset 84 (uses chkval from step 1)
	buildSignature(frame, token, sqnum)

	// Step 3: Apply outer frame encryption: mode-1 RC5 → encrypt_id
	pwdKey := NewPasswordKey()
	EncryptFrame(frame) // mode 1: frame-derived key
	EncryptID(frame, pwdKey)

	return frame, randomKey
}

// BuildCertifyReqRaw is like BuildCertifyReq but applies only EncryptID without
// the mode-1 frame encryption. Used for testing different encryption approaches.
func BuildCertifyReqRaw(token *AccessToken, sqnum uint32) ([]byte, [32]byte) {
	const baseEncPayload = 44
	const totalSize = FrameHeaderSize + baseEncPayload + 16 + 80

	frame := make([]byte, totalSize)
	frame[0] = ProtoPlaintext
	frame[1] = SubTypeSessionInit
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
	binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	randBits := uint32(mrand.Intn(0x7FFF)) << 1
	flags := randBits | (1 << 16) | (3 << 18) | FlagHasSignature | FlagHasNTPTime
	binary.LittleEndian.PutUint32(frame[20:24], flags)

	binary.LittleEndian.PutUint32(frame[24:28], 0x00000001)

	var randomKey [32]byte
	rand.Read(randomKey[:])

	hash := GiotHashString(randomKey[:])
	binary.LittleEndian.PutUint32(frame[28:32], hash)

	copy(frame[32:64], randomKey[:])
	token.TokenKey.EncryptBlock16(frame[32:48])
	token.TokenKey.EncryptBlock16(frame[48:64])

	binary.LittleEndian.PutUint32(frame[64:68], 1032)

	ntpTime := uint64(time.Now().UnixMilli())
	binary.LittleEndian.PutUint64(frame[68:76], ntpTime)

	// Same corrected order: checksum → signature → encrypt
	pwdKey := NewPasswordKey()
	InitChkval(frame)
	buildSignature(frame, token, sqnum)
	EncryptID(frame, pwdKey)

	return frame, randomKey
}

// buildSignature generates the 80-byte signature for CertifyReq frames.
//
// From iv_gutes_add_send_pkt @ 0x131b64 (decompiled):
//
//	[0:2]   header: {0x01, nonce_byte | 0x01}
//	[2:4]   checkval of 30 bytes: nonce(12) + plaintext_token_data[0:18]
//	[4:16]  nonce (12 bytes, random)
//	[16:64] AES-encrypted token data (48 bytes from decoded_token[0:48])
//	[64:80] HMAC-MD5 of (sqnum(4) + chkval(4) + sig[0:64] with PLAINTEXT token data)
//
// CRITICAL: InitChkval must be called BEFORE this function so frame[16:20] has the checksum.
// CRITICAL: HMAC is computed on PLAINTEXT token data, then AES encryption happens after.
func buildSignature(frame []byte, token *AccessToken, sqnum uint32) {
	sigStart := len(frame) - 80
	sig := frame[sigStart:]

	// Generate random nonce (12 bytes)
	var nonce [12]byte
	rand.Read(nonce[:])

	// Header
	sig[0] = 0x01
	sig[1] = nonce[0] | 0x01

	// Nonce at sig[4:16]
	copy(sig[4:16], nonce[:])

	// Token data: first 48 bytes of the decoded token (NOT tokenKeyRaw!)
	// From decompilation: memcpy(auStack_68, session+0x2fc, 0x30)
	// session+0x2fc = decoded_token[0:48]
	var tokenData [48]byte
	copy(tokenData[:], token.TokenBytes[:48])

	// Place plaintext token data in sig temporarily (for nonce checkval computation)
	copy(sig[16:64], tokenData[:])

	// Nonce checkval: get_chkval(&local_74, 0x1e) where 0x1e = 30 SHORTS = 60 bytes.
	// Covers sig[4:64] = nonce(12) + all 48 bytes of plaintext token data.
	nonceCkval := getSignatureCheckval(sig[4:64]) // 60 bytes = 30 shorts
	binary.LittleEndian.PutUint16(sig[2:4], nonceCkval)

	// Build HMAC data: sqnum(4) + chkval(4) + sig[0:64] (with PLAINTEXT token data)
	// From decompilation: HMAC is computed BEFORE AES encryption
	var hmacData [72]byte
	binary.LittleEndian.PutUint32(hmacData[0:4], sqnum)
	// Use the ACTUAL checksum from frame[16:20] (InitChkval was called before us)
	copy(hmacData[4:8], frame[16:20])
	copy(hmacData[8:72], sig[0:64])

	// Compute HMAC-MD5: key = token_key_raw (16 bytes), output = 16 bytes
	mac := hmac.New(md5.New, token.TokenKeyRaw[:])
	mac.Write(hmacData[:])
	hmacResult := mac.Sum(nil)

	// NOW AES-CBC encrypt the token data with nonce as key.
	// giote_AES_encrypt(auStack_68, 0x30, &local_74, 0x0c, 1)
	// Key is the 12-byte nonce — deriveAESKey transforms it to 16 bytes.
	// IV is "iotVideo" + 8 zero bytes. Mode is AES-128-CBC.
	aesEncryptCBC(tokenData[:], nonce[:])
	copy(sig[16:64], tokenData[:])

	// Place HMAC result at sig[64:80]
	copy(sig[64:80], hmacResult)
}

// getSignatureCheckval computes a checkval by summing 16-bit words.
// From get_chkval: param_2 is a count of shorts (2-byte words), NOT bytes.
// When called with 0x1e (30), it reads 30 shorts = 60 bytes.
func getSignatureCheckval(data []byte) uint16 {
	var sum uint16
	for i := 0; i+2 <= len(data); i += 2 {
		sum += binary.LittleEndian.Uint16(data[i : i+2])
	}
	return sum
}

// deriveAESKey transforms a non-standard-length key into a 16-byte AES key.
// From FUN_0012d694 in libiotp2pav.so: computes 4 independent hash values
// (Bernstein, giot_hash, djb2, SDBM-like) and concatenates them as LE uint32s.
func deriveAESKey(key []byte) [16]byte {
	var hash1 uint32 = 0
	var hash2 uint32 = 0x4e67c6a7 // same seed as GiotHashString
	var hash3 uint32 = 0x1505      // djb2 seed
	var hash4 uint32 = 0

	for _, b := range key {
		v := uint32(b)
		hash1 = hash1*0x83 + v
		hash2 = hash2 ^ (v + hash2*0x20 + (hash2 >> 2))
		hash3 = v + hash3*0x21
		hash4 = v + hash4*0x1003f
	}

	var out [16]byte
	binary.LittleEndian.PutUint32(out[0:4], hash1)
	binary.LittleEndian.PutUint32(out[4:8], hash2)
	binary.LittleEndian.PutUint32(out[8:12], hash3)
	binary.LittleEndian.PutUint32(out[12:16], hash4)
	return out
}

// aesEncryptCBC encrypts data in-place using AES-128-CBC mode.
// From giote_AES_encrypt: uses mbedtls_aes_crypt_cbc with IV "iotVideo"+zeros.
// For key lengths not in {16, 24, 32}, derives a 16-byte key via deriveAESKey.
func aesEncryptCBC(data, key []byte) {
	var aesKey []byte
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		derived := deriveAESKey(key)
		aesKey = derived[:]
	} else {
		aesKey = key
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return
	}

	// IV from binary: "iotVideo" + 8 null bytes (verified at 0x001bf1b9)
	iv := []byte("iotVideo\x00\x00\x00\x00\x00\x00\x00\x00")

	mode := cipher.NewCBCEncrypter(block, iv)
	// CBC requires data length to be multiple of block size (16)
	// Our data is 48 bytes = 3 blocks, so this is fine
	mode.CryptBlocks(data, data)
}

// ParseCertifyResp parses a CertifyResp frame and extracts the session ID.
//
// From gutes_on_respfrm_certify_resp @ 0x135324:
//   - err_code at payload offset 0x1a (frame[26:28]) — 0 = success
//   - session_id at payload offset 0x1c (frame[28:36]) — 8 bytes
//   - On success: sets session key = the random key from CertifyReq
func ParseCertifyResp(frame []byte, randomKey [32]byte, pwdKey *RC5Key) (*CertifyResult, error) {
	if len(frame) < FrameHeaderSize {
		return nil, fmt.Errorf("CertifyResp too short: %d bytes", len(frame))
	}

	// Decrypt the frame
	DecryptID(frame, pwdKey)
	DecryptFrame(frame)

	// Check error code at offset 26 (frame[24+2])
	if len(frame) < 28 {
		return nil, fmt.Errorf("CertifyResp too short for error code")
	}

	// The opt_flags/err_code area starts at frame[24]
	// In response, frame[26:28] contains the error code
	errCode := binary.LittleEndian.Uint16(frame[26:28])
	if errCode != 0 {
		if errCode == 3 {
			return nil, fmt.Errorf("CertifyResp: kicked by server (err=3)")
		}
		return nil, fmt.Errorf("CertifyResp: error code %d (0x%04X)", errCode, errCode)
	}

	result := &CertifyResult{}
	result.RandomKey = randomKey

	// Extract session_id from frame[28:36]
	if len(frame) >= 36 {
		result.SessionID = binary.LittleEndian.Uint64(frame[28:36])
	}

	// Session key = the 32-byte random key, used as RC5-32 key
	result.SessionKey = NewRC5Key(randomKey[:])

	return result, nil
}

// BuildInitInfoMsg constructs an init_info_msg frame (sub_type=0xA6).
//
// From gat_send_init_info_msg @ 0x13ed84:
//
//	[0]     proto = 0x7F (changed to 0x7E after session established)
//	[1]     sub_type = 0xA6
//	[2:4]   frame_len
//	[4:8]   word1 = accessID low 32 bits
//	[8:12]  word2 = accessID high 32 bits
//	[12:16] sqnum
//	[16:20] chkval
//	[20:24] flags = 0x00020000 (encrypt_mode=2)
//	--- Payload ---
//	[24:28] opt_flags (bits: 1=gdm, 2=credential_A, 3=sub_mode, 4=cred_A, 5=cred_B)
//	[27]    sub_mode = 2
//	[28:56] base payload (zeros)
//	[56:58] credential_A (2 bytes, from session config)
//	[58:62] credential_B (4 bytes, from session config)
//
// From decompiled gat_send_init_info_msg: proto is ALWAYS 0x7F and w1/w2 is ALWAYS
// the accessId — NOT the sessionID. The only post-session change is encrypt_mode=2 in flags.
func BuildInitInfoMsg(token *AccessToken, sessionID uint64, sqnum uint32, sessionKey, pwdKey *RC5Key) []byte {
	const totalSize = 62

	frame := make([]byte, totalSize)

	// Proto is always 0x7F, w1/w2 is always the accessId
	frame[0] = ProtoPlaintext // 0x7F (confirmed from decompilation)
	binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
	binary.LittleEndian.PutUint32(frame[8:12], token.Word2())

	frame[1] = SubTypeInitInfoMsg // 0xA6
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags: encrypt_mode=2 (bits 16-17 = 0x20000)
	flags := uint32(2 << 16)
	binary.LittleEndian.PutUint32(frame[20:24], flags)

	// opt_flags at payload[0:4] (frame[24:28])
	// bits 1,2,3,4,5 set = 0x3E
	optFlags := uint32(0x3E)
	binary.LittleEndian.PutUint32(frame[24:28], optFlags)

	// sub_mode at frame[27] = 2
	frame[27] = 2

	// Credential fragments: for now use zeros (need real values from session config)
	// credential_A at frame[56:58] = 0x0000
	// credential_B at frame[58:62] = 0x00000000

	// Encrypt with mode 2 (session key)
	EncryptFrameMode2(frame, sessionKey, pwdKey)

	return frame
}

// BuildSubscribeDevID constructs a subscribe frame in devid mode (sub_type=0xB0).
//
// From giot_eif_subscribe_dev @ 0x1769a4 (decompiled):
//
//	Buffer layout (relative to frame start at buffer+0x1B0):
//	  [0]     proto = 0x7F
//	  [1]     sub = 0xB0
//	  [2:4]   frame_len = 0x24 (36 bytes)
//	  [4:12]  w1/w2 = accessId
//	  [12:16] sqnum
//	  [16:20] chkval
//	  [20:24] flags (encrypt_mode=2)
//	  [25]    bit 0 = 1 (devid mode indicator)
//	  [28:36] devID (uint64, the device to subscribe to)
//
// iv_gutes_add_send_pkt sets: send_type=3 (bits 18-19), retry_interval=0x50, retries=3.
// When useSessionID is true, uses proto=0x7E and w1/w2=sessionID (post-InitInfoResp state).
func BuildSubscribeDevID(token *AccessToken, sessionID uint64, sqnum uint32, devID uint64, useSessionID bool, sessionKey, pwdKey *RC5Key) []byte {
	const totalSize = 36 // 0x24

	frame := make([]byte, totalSize)

	if useSessionID {
		frame[0] = ProtoEncrypted // 0x7E (after session state==2)
		binary.LittleEndian.PutUint32(frame[4:8], uint32(sessionID))
		binary.LittleEndian.PutUint32(frame[8:12], uint32(sessionID>>32))
	} else {
		frame[0] = ProtoPlaintext // 0x7F (same as working InitInfoMsg)
		binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
		binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	}

	frame[1] = SubTypeSubscribe // 0xB0
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags: encrypt_mode=2 (bits 16-17)
	binary.LittleEndian.PutUint32(frame[20:24], uint32(2<<16))

	// DevID mode: frame[25] bit 0 = 1
	frame[25] = 0x01

	// Device ID at frame[28:36]
	binary.LittleEndian.PutUint64(frame[28:36], devID)

	EncryptFrameMode2(frame, sessionKey, pwdKey)

	return frame
}

// BuildSubscribeTokens constructs a subscribe frame in token mode (sub_type=0xB0).
//
// From giot_eif_subscribe_dev @ 0x1769a4:
//
//	Token mode (param_2!=NULL and param_4==0):
//	  frame_len = data_len + 0x1C (28)
//	  frame[24] = device_count = data_len / 0x50 (80 bytes per device)
//	  frame[28:] = token data (80 bytes per device entry)
func BuildSubscribeTokens(token *AccessToken, sessionID uint64, sqnum uint32, tokenData []byte, useSessionID bool, sessionKey, pwdKey *RC5Key) []byte {
	totalSize := len(tokenData) + 28 // header(24) + payload_header(4) + data

	frame := make([]byte, totalSize)

	if useSessionID {
		frame[0] = ProtoEncrypted
		binary.LittleEndian.PutUint32(frame[4:8], uint32(sessionID))
		binary.LittleEndian.PutUint32(frame[8:12], uint32(sessionID>>32))
	} else {
		frame[0] = ProtoPlaintext
		binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
		binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	}

	frame[1] = SubTypeSubscribe // 0xB0
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags: encrypt_mode=2
	binary.LittleEndian.PutUint32(frame[20:24], uint32(2<<16))

	// Token mode: device_count at frame[24]
	frame[24] = byte(len(tokenData) / 80)

	// Token data at frame[28:]
	copy(frame[28:], tokenData)

	EncryptFrameMode2(frame, sessionKey, pwdKey)

	return frame
}

// DeviceInfo holds a device's TID and name from InitInfoResp.
type DeviceInfo struct {
	TID  uint64 // 8-byte device TID (used as dst_id in CALLING)
	Name string // Device name (e.g. "GW_YOUR_CAMERA_ID")
}

// ParseInitInfoResp parses an InitInfoResp (0xA7) payload to extract device TID mappings.
//
// Payload structure (from live traffic analysis, verified with 2-device payload):
//
//	[0:4]   opt_flags
//	[4:8]   device_count (uint32 LE)
//	Per device (44 bytes each):
//	  [0:4]  TID low 32 bits
//	  [4:8]  TID high 32 bits (account ID fragment)
//	  [8:10] field (online status / type)
//	  [10:12] device_attr (uint16 LE, device attribute — NOT name length)
//	  [12:44] device name (32 bytes, null-padded)
func ParseInitInfoResp(payload []byte) []DeviceInfo {
	if len(payload) < 8 {
		return nil
	}

	count := int(binary.LittleEndian.Uint32(payload[4:8]))
	if count < 1 || count > 32 {
		hexLen := len(payload)
		if hexLen > 16 {
			hexLen = 16
		}
		log.Printf("[InitInfoResp] bad count=%d (payload %d bytes, first 16: %x)", count, len(payload), payload[:hexLen])
		return nil
	}

	const nameFieldLen = 32 // Fixed 32-byte name field (null-padded)
	const entryLen = 8 + 2 + 2 + nameFieldLen // 44 bytes per device entry

	var devices []DeviceInfo
	off := 8
	for i := 0; i < count; i++ {
		if off+entryLen > len(payload) {
			break
		}
		tidLow := binary.LittleEndian.Uint32(payload[off : off+4])
		tidHigh := binary.LittleEndian.Uint32(payload[off+4 : off+8])
		tid := uint64(tidHigh)<<32 | uint64(tidLow)
		off += 12 // skip TID(8) + field(2) + attr(2)

		// Extract null-terminated name from fixed 32-byte field
		nameBytes := payload[off : off+nameFieldLen]
		name := ""
		for j, b := range nameBytes {
			if b == 0 {
				name = string(nameBytes[:j])
				break
			}
			if j == len(nameBytes)-1 {
				name = string(nameBytes)
			}
		}
		off += nameFieldLen

		log.Printf("[InitInfoResp] device %d: %q TID=0x%016X", i, name, tid)
		devices = append(devices, DeviceInfo{TID: tid, Name: name})
	}
	return devices
}

// BuildCallingMsg constructs a CALLING frame (sub_type=0xA4, 128 bytes).
//
// From iv_init_frm_CALLING @ 0x14fb30 (decompiled):
//
//	[0]     proto = 0x7F (or 0x7E after session established)
//	[1]     sub = 0xA4
//	[2:4]   frame_len = 0x80 (128)
//	[4:12]  w1/w2 = accessId (or routingSessionID after session established)
//	[12:16] sqnum
//	[16:20] chkval
//	[20:24] flags (encrypt_mode=2, send_type=1 in bits 18-19)
//	[24:26] opt_flags = 0x0001 (bit 0 set)
//	[28:32] link_id (random session ID, uint32)
//	[32:40] src_id = accessId
//	[40:48] dst_id = target device TID
//	[50]    bit 0 = 1
//	[54:56] LAN port (0 for relay-only)
//	[64:68] local IPv4 (0 for relay-only)
//
// After session established (iv_gutes_add_send_pkt at 0x131b64):
//   - proto switches to 0x7E
//   - w1/w2 switches to routingSessionID (from 0x0D SessionResp frame[28:36])
//   - flags bits 18-19 = send_type (1 for relay CALLING)
//
// routingSessionID: 8-byte session routing ID from the 0x0D SessionResp.
// If 0, uses proto=0x7F / accessId (pre-session mode).
//
// lanIP: optional LAN IPv4 address (4 bytes, network byte order at frame[64:68]).
// lanPort: optional LAN port (big-endian at frame[54:56], matching MTP_RES_RESPONSE convention).
// When lanIP is non-nil the camera knows we're on the same LAN and may use direct connectivity.
func BuildCallingMsg(token *AccessToken, routingSessionID uint64, sqnum uint32, linkID uint32, dstID uint64, sessionKey, pwdKey *RC5Key, lanIP net.IP, lanPort uint16, mtpRC5Key []byte, optLanCallFlags ...bool) []byte {
	const totalSize = 128 // 0x80

	frame := make([]byte, totalSize)

	frame[1] = SubTypeCalling // 0xA4
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// After session established, use proto=0x7E with routing session ID
	if routingSessionID != 0 {
		frame[0] = ProtoEncrypted // 0x7E
		binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
		binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	} else {
		frame[0] = ProtoPlaintext // 0x7F
		binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
		binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	}

	// Flags: encrypt_mode=2 (bits 16-17) + send_type=1 (bits 18-19)
	flags := uint32(2<<16) | uint32(1<<18)
	binary.LittleEndian.PutUint32(frame[20:24], flags)

	// opt_flags at frame[24:26]: bit 0 = 1 (always set)
	optFlags := uint16(0x0001)
	setLanCall := true
	if len(optLanCallFlags) > 0 {
		setLanCall = optLanCallFlags[0]
	}
	if ip4 := lanIP.To4(); ip4 != nil && setLanCall {
		optFlags |= 0x0002
	}
	binary.LittleEndian.PutUint16(frame[24:26], optFlags)

	// link_id at frame[28:32]
	binary.LittleEndian.PutUint32(frame[28:32], linkID)

	// src_id at frame[32:40]: accessId
	binary.LittleEndian.PutUint64(frame[32:40], token.AccessID)

	// dst_id at frame[40:48]: target device TID
	binary.LittleEndian.PutUint64(frame[40:48], dstID)

	// frame[50] bit 0 = 1
	frame[50] = frame[50] | 0x01

	// LAN address
	if ip4 := lanIP.To4(); ip4 != nil {
		copy(frame[64:68], ip4)
		binary.LittleEndian.PutUint16(frame[54:56], lanPort)
	}

	// MTP session RC5 key at frame[120:128] — 8-byte key for AV/CMD encryption.
	// Camera reads this to set up its MTP session RC5 context (iv_mtp_session_new).
	if len(mtpRC5Key) >= 8 {
		copy(frame[120:128], mtpRC5Key[:8])
	}

	// Encrypt with mode 2 (session key)
	EncryptFrameMode2(frame, sessionKey, pwdKey)

	return frame
}

// BuildNetworkDetectProbe constructs a network detect probe frame (sub_type=0xB9).
//
// This is the "P2P inner message" that the SDK sends THROUGH the P2P server to the
// camera, telling it about our LAN presence. Without this probe, the camera never
// knows we're on the same LAN and won't attempt direct LAN connectivity.
//
// From giot_init_frm_p2p_inner_msg @ 0x150c08:
//
//	Base frame (0x4C = 76 bytes):
//	  [0]     proto = 0x7F (switched to 0x7E by iv_gutes_add_send_pkt when session active)
//	  [1]     sub_type = 0xB9
//	  [2:4]   frame_len = 0xD0 (208 = 0x4C base + 0x84 payload)
//	  [4:12]  w1/w2 = routingSessionID (post-session) or accessId
//	  [12:16] sqnum
//	  [16:20] chkval
//	  [20:24] flags: encrypt_mode=1, send_type=1, bit 25, random bits 1-15
//	  --- P2P inner message payload ---
//	  [24:28] inner_flags = 0x03 (bits 0 and 1 set)
//	  [28:36] dst_id (target device TID)
//	  [36:44] caller_id = accessId
//	  [44:48] zero
//	  [48:52] zero
//	  [52]    type = 5 (network detect)
//	  [53:56] zero
//	  [56:60] zero
//	  [60:76] zero (padding to 0x4C)
//
// From giot_eif_send_network_detect @ 0x17d0b8:
//
//	Appended payload (0x84 = 132 bytes at offset 0x4C):
//	  [76:78]   timeoutMs (LE uint16, e.g. 3000)
//	  [78:80]   probeCount (LE uint16, e.g. 5)
//	  [80:208]  IP address string (null-terminated, zero-padded, 128 bytes)
//
// Encryption: mode 1 (frame-derived RC5 key), NOT mode 2 (session key).
// This is explicitly set by giot_eif_send_network_detect and preserved by
// iv_gutes_add_send_pkt (which only overrides if mode != 1).
func BuildNetworkDetectProbe(token *AccessToken, routingSessionID uint64, sqnum uint32, dstID uint64, pwdKey *RC5Key, lanIP string, probeCount uint16, timeoutMs uint16) []byte {
	const baseSize = 0x4C     // 76 bytes (P2P inner msg header)
	const payloadSize = 0x84  // 132 bytes (network detect payload)
	const totalSize = baseSize + payloadSize // 0xD0 = 208 bytes

	frame := make([]byte, totalSize)

	// --- giot_init_frm_p2p_inner_msg ---
	frame[0] = ProtoPlaintext       // 0x7F (may be switched to 0x7E below)
	frame[1] = SubTypeNetworkDetect // 0xB9
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))

	// w1/w2: after session established, use routing session ID
	if routingSessionID != 0 {
		frame[0] = ProtoEncrypted // 0x7E
		binary.LittleEndian.PutUint32(frame[4:8], uint32(routingSessionID))
		binary.LittleEndian.PutUint32(frame[8:12], uint32(routingSessionID>>32))
	} else {
		binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
		binary.LittleEndian.PutUint32(frame[8:12], token.Word2())
	}

	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags from giot_eif_send_network_detect + iv_gutes_add_send_pkt:
	//   bits 1-15:  random (bit 0 not set, so random fills 1-15)
	//   bits 16-17: encrypt_mode = 1 (frame-derived key)
	//   bits 18-19: send_type = 1 (reliable)
	//   bit 25:     set by giot_eif_send_network_detect (0x2000000)
	randBits := uint32(mrand.Intn(0x7FFF)) << 1
	flags := randBits | (1 << 16) | (1 << 18) | (1 << 25)
	binary.LittleEndian.PutUint32(frame[20:24], flags)

	// Inner message header (payload starting at offset 24)
	// inner_flags at [24:28] = 0x03 (bits 0 and 1 set)
	binary.LittleEndian.PutUint32(frame[24:28], 0x03)

	// dst_id at [28:36]
	binary.LittleEndian.PutUint64(frame[28:36], dstID)

	// caller_id at [36:44] = accessId
	binary.LittleEndian.PutUint64(frame[36:44], token.AccessID)

	// [44:48] = zero (already zero)
	// type at [52] = 5 (network detect)
	frame[52] = 5
	// [56:60] = zero (param_5)

	// --- giot_eif_send_network_detect payload ---
	// 0x84 bytes appended at offset 0x4C (baseSize)
	// [0:2] = timeoutMs (LE uint16)
	binary.LittleEndian.PutUint16(frame[baseSize:baseSize+2], timeoutMs)
	// [2:4] = probeCount (LE uint16)
	binary.LittleEndian.PutUint16(frame[baseSize+2:baseSize+4], probeCount)
	// [4:132] = IP address string (null-terminated, already zero-padded)
	copy(frame[baseSize+4:], []byte(lanIP))

	// Encrypt with mode 1 (frame-derived RC5 key) + EncryptID
	// From iv_gutes_add_send_pkt: mode 1 uses EncryptFrame, then EncryptID
	EncryptFrameFull(frame, pwdKey)

	return frame
}

// BuildHeartbeat constructs a heartbeat frame (sub_type=0xA0).
//
// From gat_send_heart_frm @ 0x146ef8:
//
//	proto=0x7F, sub_type=0xA0, frame_len=0x2C (44 bytes)
func BuildHeartbeat(token *AccessToken, sessionID uint64, sqnum uint32, sessionKey, pwdKey *RC5Key) []byte {
	const totalSize = 44

	frame := make([]byte, totalSize)

	// Proto is always 0x7F, w1/w2 is always the accessId (from decompiled gat_send_heart_frm)
	frame[0] = ProtoPlaintext // 0x7F
	binary.LittleEndian.PutUint32(frame[4:8], token.Word1())
	binary.LittleEndian.PutUint32(frame[8:12], token.Word2())

	frame[1] = SubTypeHeartbeat // 0xA0
	binary.LittleEndian.PutUint16(frame[2:4], uint16(totalSize))
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)

	// Flags: encrypt_mode=2
	binary.LittleEndian.PutUint32(frame[20:24], uint32(2<<16))

	// Encrypt with mode 2
	EncryptFrameMode2(frame, sessionKey, pwdKey)

	return frame
}
