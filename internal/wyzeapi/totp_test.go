package wyzeapi

import (
	"testing"
)

func TestGenerateTOTP_Format(t *testing.T) {
	// Known test secret (base32 of "12345678901234567890")
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	code, err := generateTOTP(secret)
	if err != nil {
		t.Fatalf("generateTOTP: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("code length = %d, want 6", len(code))
	}
	// Should be all digits
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Errorf("code contains non-digit: %c", c)
		}
	}
}

func TestGenerateTOTP_SpacesStripped(t *testing.T) {
	// Same secret with spaces should produce same result
	secret1 := "GEZDGNBVGY3TQOJQ"
	secret2 := "GEZD GNBV GY3T QOJQ"

	code1, err := generateTOTP(secret1)
	if err != nil {
		t.Fatal(err)
	}
	code2, err := generateTOTP(secret2)
	if err != nil {
		t.Fatal(err)
	}
	if code1 != code2 {
		t.Errorf("spaces should not affect result: %q vs %q", code1, code2)
	}
}

func TestGenerateTOTP_InvalidSecret(t *testing.T) {
	_, err := generateTOTP("!!not-base32!!")
	if err == nil {
		t.Error("invalid secret should error")
	}
}

func TestGenerateTOTP_Deterministic(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQ"

	// Two calls within the same 30s window should match
	code1, _ := generateTOTP(secret)
	code2, _ := generateTOTP(secret)
	if code1 != code2 {
		t.Errorf("same window should produce same code: %q vs %q", code1, code2)
	}
}
