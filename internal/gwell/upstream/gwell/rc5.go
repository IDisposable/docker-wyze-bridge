package gwell

import "encoding/binary"

// RC5-32/6/8 implementation for GUTES frame encryption.
//
// Parameters: W=32 (word size), R=6 (rounds), b=variable key bytes.
// The libiotp2pav.so binary uses rc5_ctx_new(8, 6) — 6 rounds.
//
// Key derivation for frame encryption (mode 1):
//   key = frame[0:4] + frame[0x14:0x17] + 0x00  (8 bytes)
// Encrypted regions:
//   frame[0x0C:0x14]  (sqnum + chkval = 8 bytes)
//   frame[0x18+i*8]   (payload in 8-byte blocks)
//
// Password key for ID encryption:
//   key = "www.gwell.cc" (12 bytes, hardcoded in libiotp2pav.so .rodata at 0xbfe36)
// Used by encrypt_id to encrypt word1/word2 in frame headers:
//   1. RC5 encrypt frame[4:12] with password key
//   2. XOR frame[4:8] ^= frame[0x0C:0x10] (encrypted sqnum)
//   3. XOR frame[8:12] ^= frame[0x10:0x14] (encrypted chkval)

const (
	rc5W      = 32
	rc5R      = 6 // 6 rounds (confirmed from binary: rc5_ctx_new(8, 6))
	rc5P32    = 0xB7E15163
	rc5Q32    = 0x9E3779B9
	rc5SubLen = 2 * (rc5R + 1) // 14 subkeys
)

// PasswordKeyBytes is the hardcoded default password key from libiotp2pav.so.
// Used by iv_gute_frm_encrypt_id to encrypt word1/word2 in frame headers.
// Located at .rodata offset 0xbfe36 in the ARM64 binary.
var PasswordKeyBytes = []byte("www.gwell.cc")

// RC5Key holds the expanded subkey table.
type RC5Key struct {
	S [rc5SubLen]uint32
}

func rotl32(x uint32, n uint32) uint32 {
	n &= 31
	return (x << n) | (x >> (32 - n))
}

func rotr32(x uint32, n uint32) uint32 {
	n &= 31
	return (x >> n) | (x << (32 - n))
}

// NewRC5Key performs the RC5 key expansion for a variable-length key.
func NewRC5Key(key []byte) *RC5Key {
	b := len(key)
	u := rc5W / 8 // 4
	c := b / u
	if b%u != 0 {
		c++
	}
	if c == 0 {
		c = 1
	}

	L := make([]uint32, c)
	for i := b - 1; i >= 0; i-- {
		L[i/u] = (L[i/u] << 8) + uint32(key[i])
	}

	t := rc5SubLen
	var k RC5Key
	k.S[0] = rc5P32
	for i := 1; i < t; i++ {
		k.S[i] = k.S[i-1] + rc5Q32
	}

	var A, B uint32
	var ii, j int
	n := 3 * t
	if 3*c > n {
		n = 3 * c
	}
	for s := 0; s < n; s++ {
		A = rotl32(k.S[ii]+A+B, 3)
		k.S[ii] = A
		B = rotl32(L[j]+A+B, (A+B)&31)
		L[j] = B
		ii = (ii + 1) % t
		j = (j + 1) % c
	}

	return &k
}

// NewPasswordKey creates an RC5 key from the hardcoded password.
func NewPasswordKey() *RC5Key {
	return NewRC5Key(PasswordKeyBytes)
}

// EncryptBlock encrypts an 8-byte block (two little-endian uint32 words).
func (k *RC5Key) EncryptBlock(block []byte) {
	A := binary.LittleEndian.Uint32(block[0:4])
	B := binary.LittleEndian.Uint32(block[4:8])

	A += k.S[0]
	B += k.S[1]
	for i := 1; i <= rc5R; i++ {
		A = rotl32(A^B, B&31) + k.S[2*i]
		B = rotl32(B^A, A&31) + k.S[2*i+1]
	}

	binary.LittleEndian.PutUint32(block[0:4], A)
	binary.LittleEndian.PutUint32(block[4:8], B)
}

// DecryptBlock decrypts an 8-byte block (two little-endian uint32 words).
func (k *RC5Key) DecryptBlock(block []byte) {
	A := binary.LittleEndian.Uint32(block[0:4])
	B := binary.LittleEndian.Uint32(block[4:8])

	for i := rc5R; i >= 1; i-- {
		B = rotr32(B-k.S[2*i+1], A&31) ^ A
		A = rotr32(A-k.S[2*i], B&31) ^ B
	}
	B -= k.S[1]
	A -= k.S[0]

	binary.LittleEndian.PutUint32(block[0:4], A)
	binary.LittleEndian.PutUint32(block[4:8], B)
}

// FrameRC5Key derives the RC5 key from a GUTES frame header.
// key = frame[0:4] + frame[0x14:0x17] + 0x00
func FrameRC5Key(frame []byte) *RC5Key {
	if len(frame) < 0x17 {
		return nil
	}
	key := [8]byte{
		frame[0], frame[1], frame[2], frame[3],
		frame[0x14], frame[0x15], frame[0x16], 0,
	}
	return NewRC5Key(key[:])
}

// EncryptFrame encrypts a GUTES frame in-place (mode 1 only).
// Encrypts frame[0x0C:0x14] (sqnum+chkval) and payload 8-byte blocks
// within the encrypted data region (determined by flags).
// Does NOT encrypt word1/word2 (use EncryptID for that).
func EncryptFrame(frame []byte) {
	k := FrameRC5Key(frame)
	if k == nil {
		return
	}
	if len(frame) >= 0x14 {
		k.EncryptBlock(frame[0x0C:0x14])
	}
	encLen := GetEncryptDataLen(frame)
	end := 0x18 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for off := 0x18; off+8 <= end; off += 8 {
		k.EncryptBlock(frame[off : off+8])
	}
}

// DecryptFrame decrypts a GUTES frame in-place (mode 1 only).
// Decrypts frame[0x0C:0x14] (sqnum+chkval) and payload 8-byte blocks
// within the encrypted data region (determined by flags).
// Does NOT decrypt word1/word2 (use DecryptID for that).
func DecryptFrame(frame []byte) {
	k := FrameRC5Key(frame)
	if k == nil {
		return
	}
	if len(frame) >= 0x14 {
		k.DecryptBlock(frame[0x0C:0x14])
	}
	encLen := GetEncryptDataLen(frame)
	end := 0x18 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for off := 0x18; off+8 <= end; off += 8 {
		k.DecryptBlock(frame[off : off+8])
	}
}

// EncryptID encrypts word1/word2 in a GUTES frame using the password key.
// Must be called AFTER EncryptFrame (mode-1 encryption), because it uses
// the already-encrypted sqnum/chkval at frame[0x0C:0x14].
//
// Algorithm (from iv_gute_frm_encrypt_id disassembly):
//   1. RC5 encrypt frame[4:12] (word1+word2) with password key
//   2. frame[4:8]  ^= frame[0x0C:0x10]  (XOR with encrypted sqnum)
//   3. frame[8:12] ^= frame[0x10:0x14]  (XOR with encrypted chkval)
func EncryptID(frame []byte, pwdKey *RC5Key) {
	if len(frame) < 0x14 || pwdKey == nil {
		return
	}
	// Step 1: RC5 encrypt word1+word2
	pwdKey.EncryptBlock(frame[4:12])

	// Step 2-3: XOR with encrypted sqnum/chkval
	w1 := binary.LittleEndian.Uint32(frame[4:8])
	w2 := binary.LittleEndian.Uint32(frame[8:12])
	encSq := binary.LittleEndian.Uint32(frame[0x0C:0x10])
	encCk := binary.LittleEndian.Uint32(frame[0x10:0x14])
	binary.LittleEndian.PutUint32(frame[4:8], w1^encSq)
	binary.LittleEndian.PutUint32(frame[8:12], w2^encCk)
}

// DecryptID decrypts word1/word2 in a GUTES frame using the password key.
// Must be called BEFORE DecryptFrame (mode-1 decryption).
//
// Algorithm (reverse of EncryptID):
//   1. frame[4:8]  ^= frame[0x0C:0x10]  (undo XOR with encrypted sqnum)
//   2. frame[8:12] ^= frame[0x10:0x14]  (undo XOR with encrypted chkval)
//   3. RC5 decrypt frame[4:12] (word1+word2) with password key
func DecryptID(frame []byte, pwdKey *RC5Key) {
	if len(frame) < 0x14 || pwdKey == nil {
		return
	}
	// Step 1-2: Undo XOR with encrypted sqnum/chkval
	w1 := binary.LittleEndian.Uint32(frame[4:8])
	w2 := binary.LittleEndian.Uint32(frame[8:12])
	encSq := binary.LittleEndian.Uint32(frame[0x0C:0x10])
	encCk := binary.LittleEndian.Uint32(frame[0x10:0x14])
	binary.LittleEndian.PutUint32(frame[4:8], w1^encSq)
	binary.LittleEndian.PutUint32(frame[8:12], w2^encCk)

	// Step 3: RC5 decrypt word1+word2
	pwdKey.DecryptBlock(frame[4:12])
}

// EncryptFrameFull applies the complete GUTES encryption pipeline:
//   1. Compute checksum (InitChkval)
//   2. RC5 encrypt sqnum+chkval and payload (EncryptFrame, mode 1)
//   3. Encrypt word1+word2 with password key (EncryptID)
func EncryptFrameFull(frame []byte, pwdKey *RC5Key) {
	InitChkval(frame)
	EncryptFrame(frame)
	EncryptID(frame, pwdKey)
}

// DecryptFrameFull applies the complete GUTES decryption pipeline:
//   1. Decrypt word1+word2 with password key (DecryptID)
//   2. RC5 decrypt sqnum+chkval and payload (DecryptFrame, mode 1)
//   3. Verify checksum (unless opt_resp flag bit 21 is set)
func DecryptFrameFull(frame []byte, pwdKey *RC5Key) bool {
	DecryptID(frame, pwdKey)
	DecryptFrame(frame)
	// opt_resp flag (bit 21): skip checksum if set
	if len(frame) >= 24 {
		flags := binary.LittleEndian.Uint32(frame[20:24])
		if flags>>21&1 == 1 {
			return true
		}
	}
	return VerifyChkval(frame)
}

// EncryptFrameMode2 encrypts a GUTES frame using the session key (mode 2).
// From iv_gute_frm_rc5_encrypt: mode 2 uses a pre-set session key
// for sqnum+chkval and payload, then EncryptID with password key.
func EncryptFrameMode2(frame []byte, sessionKey, pwdKey *RC5Key) {
	InitChkval(frame)
	if len(frame) >= 0x14 {
		sessionKey.EncryptBlock(frame[0x0C:0x14])
	}
	encLen := GetEncryptDataLen(frame)
	end := 0x18 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for off := 0x18; off+8 <= end; off += 8 {
		sessionKey.EncryptBlock(frame[off : off+8])
	}
	EncryptID(frame, pwdKey)
}

// DecryptFrameMode2 decrypts a GUTES frame encrypted with mode 2 (session key).
// Exact reverse of EncryptFrameMode2:
//   1. DecryptID with password key
//   2. RC5 decrypt sqnum+chkval with session key
//   3. RC5 decrypt payload with session key (8-byte blocks)
//   4. Verify checksum (unless opt_resp flag is set at bit 21)
//
// From iv_gute_frm_rc5_decrypt: when bit 21 of flags is set, checksum
// verification is skipped. Stream data (0xB1) frames use this flag.
func DecryptFrameMode2(frame []byte, sessionKey, pwdKey *RC5Key) bool {
	DecryptID(frame, pwdKey)
	if len(frame) >= 0x14 {
		sessionKey.DecryptBlock(frame[0x0C:0x14])
	}
	encLen := GetEncryptDataLen(frame)
	end := 0x18 + encLen
	if end > len(frame) {
		end = len(frame)
	}
	for off := 0x18; off+8 <= end; off += 8 {
		sessionKey.DecryptBlock(frame[off : off+8])
	}

	// Check opt_resp flag (bit 21): if set, skip checksum verification.
	// From decompiled iv_gute_frm_rc5_decrypt @ 0x12eb18:
	//   if ((*(uint *)(param_1 + 0x14) >> 0x15 & 1) == 0)
	//       && checksum_mismatch) → fail
	// So when bit 21 is 1, decrypt succeeds regardless of checksum.
	if len(frame) >= 24 {
		flags := binary.LittleEndian.Uint32(frame[20:24])
		if flags>>21&1 == 1 {
			return true
		}
	}
	return VerifyChkval(frame)
}

// --- RC5-64: 64-bit word variant for CertifyReq inner encryption ---
//
// The SDK uses RC5-64/6 (W=64, R=6) with 16-byte blocks for encrypting
// the session random key inside CertifyReq. Decompiled from rc5_ctx_enc
// at 0x15ea24 → FUN_0015edf4 (16-byte block handler).
//
// This is a separate implementation from RC5-32 because it uses uint64 words.

const (
	rc5P64 = uint64(0xB7E151628AED2A6B)
	rc5Q64 = uint64(0x9E3779B97F4A7C15)
)

// RC5Key64 holds the expanded subkey table for RC5-64.
type RC5Key64 struct {
	S [rc5SubLen]uint64 // same number of subkeys as RC5-32 (14 for R=6)
}

func rotl64(x uint64, n uint64) uint64 {
	n &= 63
	return (x << n) | (x >> (64 - n))
}

func rotr64(x uint64, n uint64) uint64 {
	n &= 63
	return (x >> n) | (x << (64 - n))
}

// NewRC5Key64 performs the RC5-64 key expansion for a variable-length key.
func NewRC5Key64(key []byte) *RC5Key64 {
	b := len(key)
	u := 8 // W/8 = 64/8 = 8 bytes per word
	c := b / u
	if b%u != 0 {
		c++
	}
	if c == 0 {
		c = 1
	}

	L := make([]uint64, c)
	for i := b - 1; i >= 0; i-- {
		L[i/u] = (L[i/u] << 8) + uint64(key[i])
	}

	t := rc5SubLen
	var k RC5Key64
	k.S[0] = rc5P64
	for i := 1; i < t; i++ {
		k.S[i] = k.S[i-1] + rc5Q64
	}

	var A, B uint64
	var ii, j int
	n := 3 * t
	if 3*c > n {
		n = 3 * c
	}
	for s := 0; s < n; s++ {
		A = rotl64(k.S[ii]+A+B, 3)
		k.S[ii] = A
		B = rotl64(L[j]+A+B, (A+B)&63)
		L[j] = B
		ii = (ii + 1) % t
		j = (j + 1) % c
	}

	return &k
}

// EncryptBlock16 encrypts a 16-byte block (two little-endian uint64 words).
func (k *RC5Key64) EncryptBlock16(block []byte) {
	A := binary.LittleEndian.Uint64(block[0:8])
	B := binary.LittleEndian.Uint64(block[8:16])

	A += k.S[0]
	B += k.S[1]
	for i := 1; i <= rc5R; i++ {
		A = rotl64(A^B, B&63) + k.S[2*i]
		B = rotl64(B^A, A&63) + k.S[2*i+1]
	}

	binary.LittleEndian.PutUint64(block[0:8], A)
	binary.LittleEndian.PutUint64(block[8:16], B)
}

// DecryptBlock16 decrypts a 16-byte block (two little-endian uint64 words).
func (k *RC5Key64) DecryptBlock16(block []byte) {
	A := binary.LittleEndian.Uint64(block[0:8])
	B := binary.LittleEndian.Uint64(block[8:16])

	for i := rc5R; i >= 1; i-- {
		B = rotr64(B-k.S[2*i+1], A&63) ^ A
		A = rotr64(A-k.S[2*i], B&63) ^ B
	}
	B -= k.S[1]
	A -= k.S[0]

	binary.LittleEndian.PutUint64(block[0:8], A)
	binary.LittleEndian.PutUint64(block[8:16], B)
}
