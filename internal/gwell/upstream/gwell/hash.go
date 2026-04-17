package gwell

// GiotHashString computes the hash used in CertifyReq frames.
// Decompiled from giot_hash_string @ 0x12e5a4 in libiotp2pav.so.
//
// Algorithm: modified DJB2 variant with seed 0x4e67c6a7.
// C code (with implicit precedence): hash = hash ^ byte + hash * 0x20 + (hash >> 2)
// C precedence: * (14) > + (13) > >> (12) > ^ (8), so XOR is outermost:
//
//	hash = hash ^ (byte + hash*0x20 + (hash >> 2))
func GiotHashString(data []byte) uint32 {
	var hash uint32 = 0x4e67c6a7
	for _, b := range data {
		hash = hash ^ (uint32(b) + hash*0x20 + (hash >> 2))
	}
	return hash
}
