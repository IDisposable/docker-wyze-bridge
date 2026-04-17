package gwell

import (
	"bytes"
	"testing"
)

func TestXORCryptRoundTrip(t *testing.T) {
	data := []byte("Hello, XOR encryption test!")
	key := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}

	original := make([]byte, len(data))
	copy(original, data)

	XORCrypt(data, key)
	if bytes.Equal(data, original) {
		t.Error("XOR did not change data")
	}

	XORCrypt(data, key)
	if !bytes.Equal(data, original) {
		t.Errorf("round-trip failed: got %x, want %x", data, original)
	}
}

func TestXORCryptEmptyKey(t *testing.T) {
	data := []byte{0x42}
	XORCrypt(data, nil)
	if data[0] != 0x42 {
		t.Error("empty key should be no-op")
	}
}

func TestXORCryptSingleByte(t *testing.T) {
	data := []byte{0xFF}
	key := []byte{0xFF}
	XORCrypt(data, key)
	if data[0] != 0x00 {
		t.Errorf("0xFF ^ 0xFF = %02x, want 0x00", data[0])
	}
}
