package gwell

// XORCrypt applies repeating-key XOR in-place.
// This is used as xor_encrypt and xor_decrypt — the operation is symmetric.
func XORCrypt(data []byte, key []byte) {
	if len(key) == 0 {
		return
	}
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
}
