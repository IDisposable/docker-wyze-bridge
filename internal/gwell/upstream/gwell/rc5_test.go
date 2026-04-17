package gwell

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Test vectors for RC5-32/6 (6 rounds, confirmed from libiotp2pav.so binary).
// Password key "www.gwell.cc" validated against disassembly at .rodata 0xbfe36.

func TestRC5PasswordKeyEncrypt(t *testing.T) {
	// Known test vector from binary analysis:
	// RC5_6_encrypt(0x001be2f4, 0x930186d6) with key "www.gwell.cc" = (0xb9692bdb, 0x84c73060)
	k := NewPasswordKey()

	block := make([]byte, 8)
	binary.LittleEndian.PutUint32(block[0:4], 0x001be2f4)
	binary.LittleEndian.PutUint32(block[4:8], 0x930186d6)

	k.EncryptBlock(block)

	gotA := binary.LittleEndian.Uint32(block[0:4])
	gotB := binary.LittleEndian.Uint32(block[4:8])
	if gotA != 0xb9692bdb || gotB != 0x84c73060 {
		t.Errorf("PasswordKey encrypt(0x001be2f4, 0x930186d6) = (0x%08x, 0x%08x), want (0xb9692bdb, 0x84c73060)",
			gotA, gotB)
	}
}

func TestRC5PasswordKeyDecrypt(t *testing.T) {
	// Reverse of the encrypt test
	k := NewPasswordKey()

	block := make([]byte, 8)
	binary.LittleEndian.PutUint32(block[0:4], 0xb9692bdb)
	binary.LittleEndian.PutUint32(block[4:8], 0x84c73060)

	k.DecryptBlock(block)

	gotA := binary.LittleEndian.Uint32(block[0:4])
	gotB := binary.LittleEndian.Uint32(block[4:8])
	if gotA != 0x001be2f4 || gotB != 0x930186d6 {
		t.Errorf("PasswordKey decrypt(0xb9692bdb, 0x84c73060) = (0x%08x, 0x%08x), want (0x001be2f4, 0x930186d6)",
			gotA, gotB)
	}
}

func TestRC5RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"8-byte key", []byte{0x42, 0x13, 0x37, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}},
		{"12-byte password key", PasswordKeyBytes},
		{"all-zero key", make([]byte, 8)},
		{"sequential key", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := NewRC5Key(tt.key)
			original := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
			block := make([]byte, 8)
			copy(block, original)

			k.EncryptBlock(block)
			if bytes.Equal(block, original) {
				t.Error("EncryptBlock did not change the data")
			}

			k.DecryptBlock(block)
			if !bytes.Equal(block, original) {
				t.Errorf("round-trip failed: got %x, want %x", block, original)
			}
		})
	}
}

func TestFrameRC5Key(t *testing.T) {
	// Frame key = frame[0:4] + frame[0x14:0x17] + 0x00
	frame := make([]byte, 40)
	frame[0] = 0x7F
	frame[1] = 0x15
	frame[2] = 0x28
	frame[3] = 0x00
	frame[0x14] = 0xAA
	frame[0x15] = 0xBB
	frame[0x16] = 0xCC

	k := FrameRC5Key(frame)
	if k == nil {
		t.Fatal("FrameRC5Key returned nil")
	}

	// Verify against key derived manually
	manualKey := []byte{0x7F, 0x15, 0x28, 0x00, 0xAA, 0xBB, 0xCC, 0x00}
	k2 := NewRC5Key(manualKey)
	for i := 0; i < rc5SubLen; i++ {
		if k.S[i] != k2.S[i] {
			t.Errorf("S[%d] mismatch: frame=0x%08X manual=0x%08X", i, k.S[i], k2.S[i])
		}
	}
}

func TestEncryptDecryptFrameRoundTrip(t *testing.T) {
	// Mode-1 encrypt then decrypt should return original
	frame := make([]byte, 40)
	frame[0] = 0x7F
	frame[1] = 0x15
	binary.LittleEndian.PutUint16(frame[2:4], 40)
	binary.LittleEndian.PutUint32(frame[4:8], 0xDEADBEEF)
	binary.LittleEndian.PutUint32(frame[8:12], 0xCAFEBABE)
	binary.LittleEndian.PutUint32(frame[12:16], 1) // sqnum
	binary.LittleEndian.PutUint32(frame[16:20], 0) // chkval
	frame[0x14] = 0x11
	frame[0x15] = 0x22
	frame[0x16] = 0x33
	frame[0x17] = 0x44
	for i := 24; i < 40; i++ {
		frame[i] = byte(i)
	}

	original := make([]byte, len(frame))
	copy(original, frame)

	EncryptFrame(frame)

	// Verify plaintext regions unchanged
	if !bytes.Equal(frame[0:12], original[0:12]) {
		t.Errorf("frame[0:12] changed: got %x, want %x", frame[0:12], original[0:12])
	}
	if !bytes.Equal(frame[20:24], original[20:24]) {
		t.Errorf("frame[20:24] changed: got %x, want %x", frame[20:24], original[20:24])
	}

	// Verify encrypted regions changed
	if bytes.Equal(frame[12:20], original[12:20]) {
		t.Error("frame[12:20] was NOT encrypted")
	}
	if bytes.Equal(frame[24:], original[24:]) {
		t.Error("frame[24:] was NOT encrypted")
	}

	// Decrypt and verify
	DecryptFrame(frame)
	if !bytes.Equal(frame, original) {
		t.Errorf("round-trip failed:\n  got  %x\n  want %x", frame, original)
	}
}

func TestEncryptIDRoundTrip(t *testing.T) {
	// Build a frame, apply full encryption, then full decryption
	frame := make([]byte, 48)
	frame[0] = 0x7F
	frame[1] = 0x01 // DetectReq2
	binary.LittleEndian.PutUint16(frame[2:4], 48)
	binary.LittleEndian.PutUint32(frame[4:8], 0x12345678)   // word1 (localID)
	binary.LittleEndian.PutUint32(frame[8:12], 0x00000000)   // word2 (remoteID)
	binary.LittleEndian.PutUint32(frame[12:16], 0x00000001)  // sqnum
	binary.LittleEndian.PutUint32(frame[20:24], 0x00010000)  // flags
	for i := 24; i < 48; i++ {
		frame[i] = byte(i)
	}

	original := make([]byte, len(frame))
	copy(original, frame)

	pwdKey := NewPasswordKey()

	// Full encrypt: checksum → mode-1 → encrypt_id
	EncryptFrameFull(frame, pwdKey)

	// frame[0:4] should be unchanged (proto, type, len)
	if !bytes.Equal(frame[0:4], original[0:4]) {
		t.Errorf("frame[0:4] changed")
	}
	// frame[20:24] should be unchanged (flags)
	if !bytes.Equal(frame[20:24], original[20:24]) {
		t.Errorf("frame[20:24] changed")
	}
	// frame[4:20] should be different (encrypted)
	if bytes.Equal(frame[4:20], original[4:20]) {
		t.Error("frame[4:20] was not encrypted")
	}

	// Full decrypt: decrypt_id → mode-1 → verify checksum
	valid := DecryptFrameFull(frame, pwdKey)
	if !valid {
		t.Error("checksum invalid after full decrypt")
	}

	// word1 should be restored
	w1 := binary.LittleEndian.Uint32(frame[4:8])
	if w1 != 0x12345678 {
		t.Errorf("word1 = 0x%08x, want 0x12345678", w1)
	}
	// word2 should be restored
	w2 := binary.LittleEndian.Uint32(frame[8:12])
	if w2 != 0x00000000 {
		t.Errorf("word2 = 0x%08x, want 0x00000000", w2)
	}
	// sqnum should be restored
	sq := binary.LittleEndian.Uint32(frame[12:16])
	if sq != 0x00000001 {
		t.Errorf("sqnum = 0x%08x, want 0x00000001", sq)
	}
}

func TestEncryptFrameNoPayloadBlocks(t *testing.T) {
	// A 24-byte frame has no payload, only encrypts [0x0C:0x14]
	frame := make([]byte, 24)
	frame[0] = 0x7F
	frame[1] = 0x01
	binary.LittleEndian.PutUint16(frame[2:4], 24)
	frame[0x14] = 0x10
	frame[0x15] = 0x20
	frame[0x16] = 0x30

	original := make([]byte, 24)
	copy(original, frame)

	EncryptFrame(frame)

	// Header unchanged
	if !bytes.Equal(frame[0:12], original[0:12]) {
		t.Error("header changed")
	}

	// sqnum+chkval encrypted
	if bytes.Equal(frame[12:20], original[12:20]) {
		t.Error("sqnum+chkval not encrypted")
	}

	// Round-trip
	DecryptFrame(frame)
	if !bytes.Equal(frame, original) {
		t.Errorf("round-trip failed: got %x, want %x", frame, original)
	}
}

func TestFrameRC5KeyNilOnShortFrame(t *testing.T) {
	short := make([]byte, 10)
	if k := FrameRC5Key(short); k != nil {
		t.Error("expected nil for short frame")
	}
}

func TestFullEncryptDecryptMultipleFrames(t *testing.T) {
	// Test that different localIDs produce different encrypted outputs
	pwdKey := NewPasswordKey()

	frame1 := makeTestFrame(0xAAAAAAAA, 0, 1)
	frame2 := makeTestFrame(0xBBBBBBBB, 0, 1)

	EncryptFrameFull(frame1, pwdKey)
	EncryptFrameFull(frame2, pwdKey)

	// Encrypted frames should differ (different localIDs → different encrypted word1/word2)
	if bytes.Equal(frame1[4:12], frame2[4:12]) {
		t.Error("different localIDs produced same encrypted word1/word2")
	}
}

func TestGetEncryptDataLen(t *testing.T) {
	tests := []struct {
		name     string
		totalLen uint16
		flags    uint32
		want     int
	}{
		{"DetectReq2 no flags", 68, 0x00010000, 44},
		{"SessInit with sig+ntp", 164, 0x014d9092, 44}, // 164-24-80-16=44
		{"SessInit sig only", 164, 0x00410000, 60},      // 164-24-80=60
		{"SessInit ntp only", 164, 0x01010000, 124},      // 164-24-16=124
		{"Small frame", 32, 0x00010000, 8},               // 32-24=8
		{"Header only", 24, 0x00010000, 0},               // 24-24=0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := make([]byte, tt.totalLen)
			frame[0] = 0x7F
			frame[1] = 0x0C
			binary.LittleEndian.PutUint16(frame[2:4], tt.totalLen)
			binary.LittleEndian.PutUint32(frame[20:24], tt.flags)
			got := GetEncryptDataLen(frame)
			if got != tt.want {
				t.Errorf("GetEncryptDataLen(totalLen=%d, flags=0x%08X) = %d, want %d",
					tt.totalLen, tt.flags, got, tt.want)
			}
		})
	}
}

func TestEncryptFrameWithSignatureFlags(t *testing.T) {
	// A 164-byte SessInit frame with has_signature+has_ntp_time flags.
	// Only 44 bytes of payload should be encrypted, not all 140.
	frame := make([]byte, 164)
	frame[0] = 0x7F
	frame[1] = 0x0C
	binary.LittleEndian.PutUint16(frame[2:4], 164)
	binary.LittleEndian.PutUint32(frame[4:8], 0x12345678)
	binary.LittleEndian.PutUint32(frame[8:12], 0)
	binary.LittleEndian.PutUint32(frame[12:16], 1) // sqnum
	// flags: opt_encrypt=1, has_signature, has_ntp_time
	binary.LittleEndian.PutUint32(frame[20:24], FlagHasSignature|FlagHasNTPTime|0x00010000)

	// Fill payload with known data
	for i := 24; i < 164; i++ {
		frame[i] = byte(i)
	}

	original := make([]byte, 164)
	copy(original, frame)

	pwdKey := NewPasswordKey()
	EncryptFrameFull(frame, pwdKey)

	// NTP time region [68:84] should be UNCHANGED (not encrypted)
	if !bytes.Equal(frame[68:84], original[68:84]) {
		t.Error("NTP time region [68:84] was encrypted — should be plaintext")
	}
	// Signature region [84:164] should be UNCHANGED (not encrypted)
	if !bytes.Equal(frame[84:164], original[84:164]) {
		t.Error("Signature region [84:164] was encrypted — should be plaintext")
	}
	// Encrypted payload [24:64] should be changed (5 full 8-byte blocks)
	if bytes.Equal(frame[24:64], original[24:64]) {
		t.Error("Encrypted payload [24:64] was NOT encrypted")
	}

	// Full decrypt should restore everything
	valid := DecryptFrameFull(frame, pwdKey)
	if !valid {
		t.Error("checksum invalid after full decrypt")
	}
	if binary.LittleEndian.Uint32(frame[4:8]) != 0x12345678 {
		t.Errorf("word1 not restored: got 0x%08x", binary.LittleEndian.Uint32(frame[4:8]))
	}
	// Verify payload restored
	for i := 24; i < 64; i++ {
		if frame[i] != original[i] {
			t.Errorf("payload byte [%d] not restored: got 0x%02x, want 0x%02x", i, frame[i], original[i])
			break
		}
	}
}

func TestDecryptPCAPSessInit(t *testing.T) {
	// Decrypt the actual SessInit frame from the PCAP to understand the payload structure.
	// This is the 164-byte client SessInit sent to the P2P server.
	pcapHex := "7f0ca400" +
		"c42ea81d99f08e74" + // word1+word2 (encrypted)
		"1f05c1a4f9c049f0" + // sqnum+chkval (encrypted)
		"92904d01" + // flags (plaintext) = 0x014d9092
		"7cc27d0dd7ba91ff" + // encrypted payload block 0
		"1a95b31d1c52761b" + // encrypted payload block 1
		"05c851ad6e434bc7" + // encrypted payload block 2
		"e4b11c12479d6e41" + // encrypted payload block 3
		"110724962ce6d0d8" + // encrypted payload block 4
		"08040000" + // payload word 10 (checksummed, not RC5-encrypted)
		"789b7b0100000000" + // NTP time (plaintext)
		"0000000000000000" + // NTP time cont (plaintext)
		"01016af9c7af96f7" + // signature (plaintext)
		"7affe64e410c6e77" +
		"d7c281bb1b1eb3fb" +
		"202a1acf68484880" +
		"67c12ad68508636c" +
		"e9d9cb45861ff3a0" +
		"45ed271d9100fe69" +
		"78f5c036114cbc1b" +
		"85e888e582965cff" +
		"f72d40aff7193092"

	frame := hexToBytes(t, pcapHex)
	if len(frame) != 164 {
		t.Fatalf("frame length = %d, want 164", len(frame))
	}

	// Verify flags
	flags := binary.LittleEndian.Uint32(frame[20:24])
	t.Logf("Flags: 0x%08X", flags)
	t.Logf("  opt_encrypt = %d", (flags>>16)&3)
	t.Logf("  has_signature = %v", flags&FlagHasSignature != 0)
	t.Logf("  has_ntp_time = %v", flags&FlagHasNTPTime != 0)

	encLen := GetEncryptDataLen(frame)
	t.Logf("Encrypted data length: %d bytes", encLen)

	pwdKey := NewPasswordKey()

	// Decrypt
	valid := DecryptFrameFull(frame, pwdKey)
	t.Logf("Checksum valid: %v", valid)

	// Log decrypted header fields
	word1 := binary.LittleEndian.Uint32(frame[4:8])
	word2 := binary.LittleEndian.Uint32(frame[8:12])
	sqnum := binary.LittleEndian.Uint32(frame[12:16])
	chkval := binary.LittleEndian.Uint32(frame[16:20])
	t.Logf("Decrypted header:")
	t.Logf("  word1 (localID)  = 0x%08X", word1)
	t.Logf("  word2 (remoteID) = 0x%08X", word2)
	t.Logf("  sqnum            = %d (0x%08X)", sqnum, sqnum)
	t.Logf("  chkval           = 0x%08X", chkval)

	// Log decrypted payload (certify data)
	timeDivField := binary.LittleEndian.Uint32(frame[24:28])
	hashField := binary.LittleEndian.Uint32(frame[28:32])
	t.Logf("Decrypted certify payload:")
	t.Logf("  [24:28] time_div_7200  = %d (0x%08X)", timeDivField, timeDivField)
	t.Logf("  [28:32] hash_value     = 0x%08X", hashField)
	t.Logf("  [32:64] encrypted_random (32 bytes, inner-encrypted with device key):")
	for i := 32; i < 64; i += 8 {
		end := i + 8
		if end > 64 {
			end = 64
		}
		t.Logf("    [%d:%d] = %x", i, end, frame[i:end])
	}

	// Word at offset 64 (checksummed but not RC5-encrypted)
	mtuField := binary.LittleEndian.Uint32(frame[64:68])
	t.Logf("  [64:68] MTU/flags      = %d (0x%08X)", mtuField, mtuField)

	// NTP time region
	ntpWord1 := binary.LittleEndian.Uint32(frame[68:72])
	ntpWord2 := binary.LittleEndian.Uint32(frame[72:76])
	ntpWord3 := binary.LittleEndian.Uint32(frame[76:80])
	ntpWord4 := binary.LittleEndian.Uint32(frame[80:84])
	t.Logf("NTP time region [68:84]:")
	t.Logf("  [68:72] = 0x%08X (%d)", ntpWord1, ntpWord1)
	t.Logf("  [72:76] = 0x%08X", ntpWord2)
	t.Logf("  [76:80] = 0x%08X", ntpWord3)
	t.Logf("  [80:84] = 0x%08X", ntpWord4)

	// Signature region (first 16 bytes)
	t.Logf("Signature region [84:164] (first 16 bytes):")
	t.Logf("  %x", frame[84:100])

	// Verify checksum is valid
	if !valid {
		t.Logf("WARNING: Checksum did not validate. This may mean:")
		t.Logf("  1. The flags interpretation is wrong")
		t.Logf("  2. The encrypt_data_len calculation is wrong")
		t.Logf("  3. The checksum mask is different for this frame type")
	}
}

func hexToBytes(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var v byte
		for j := 0; j < 2; j++ {
			c := s[i+j]
			switch {
			case c >= '0' && c <= '9':
				v = v*16 + (c - '0')
			case c >= 'a' && c <= 'f':
				v = v*16 + (c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				v = v*16 + (c - 'A' + 10)
			default:
				t.Fatalf("invalid hex char %c at position %d", c, i+j)
			}
		}
		b[i/2] = v
	}
	return b
}

func makeTestFrame(localID, remoteID, sqnum uint32) []byte {
	frame := make([]byte, 68) // DetectReq2 size
	frame[0] = 0x7F
	frame[1] = 0x01
	binary.LittleEndian.PutUint16(frame[2:4], 68)
	binary.LittleEndian.PutUint32(frame[4:8], localID)
	binary.LittleEndian.PutUint32(frame[8:12], remoteID)
	binary.LittleEndian.PutUint32(frame[12:16], sqnum)
	binary.LittleEndian.PutUint32(frame[20:24], 0x00010000)
	for i := 24; i < 68; i++ {
		frame[i] = byte(i & 0xFF)
	}
	return frame
}
