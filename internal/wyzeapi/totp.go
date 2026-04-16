package wyzeapi

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// generateTOTP generates a 6-digit TOTP code from a base32-encoded secret.
func generateTOTP(secret string) (string, error) {
	// Clean the secret: remove spaces, uppercase, strip padding
	secret = strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	secret = strings.TrimRight(secret, "=")

	// Pad to multiple of 8 for base32
	if pad := len(secret) % 8; pad != 0 {
		secret += strings.Repeat("=", 8-pad)
	}

	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("decode TOTP secret: %w", err)
	}

	// TOTP uses 30-second time steps
	counter := uint64(time.Now().Unix()) / 30

	// HOTP computation (RFC 4226)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	hash := mac.Sum(nil)

	// Dynamic truncation
	offset := hash[len(hash)-1] & 0x0f
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// 6-digit code
	otp := code % uint32(math.Pow10(6))
	return fmt.Sprintf("%06d", otp), nil
}
