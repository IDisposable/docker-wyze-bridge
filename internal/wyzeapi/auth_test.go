package wyzeapi

import (
	"testing"
)

func TestHashPassword_Plain(t *testing.T) {
	// Triple MD5 of "test123" (verified against Python implementation)
	got := hashPassword("test123")
	want := "9dd6023e88f1e5bcf51415d2caa4e031"
	if got != want {
		t.Errorf("hashPassword(\"test123\") = %q, want %q", got, want)
	}
}

func TestHashPassword_AlreadyHashed(t *testing.T) {
	got := hashPassword("md5:abc123")
	if got != "abc123" {
		t.Errorf("hashPassword(\"md5:abc123\") = %q, want \"abc123\"", got)
	}

	got = hashPassword("hashed:def456")
	if got != "def456" {
		t.Errorf("hashPassword(\"hashed:def456\") = %q, want \"def456\"", got)
	}
}

func TestHashPassword_Trimming(t *testing.T) {
	got := hashPassword("  test123  ")
	want := hashPassword("test123")
	if got != want {
		t.Errorf("hashPassword with spaces should trim: got %q, want %q", got, want)
	}
}

func TestSignMsg(t *testing.T) {
	// Test with known values
	// key = MD5("mytoken" + "wyze_app_secret_key_132")
	// sig = HMAC-MD5(hex(key), "hello")
	appID := "9319141212m2ik"
	msg := "hello"
	token := "mytoken"

	result := signMsg(appID, msg, token)
	if len(result) != 32 {
		t.Errorf("signMsg result should be 32-char hex, got len=%d: %q", len(result), result)
	}

	// Same inputs should produce same output (deterministic)
	result2 := signMsg(appID, msg, token)
	if result != result2 {
		t.Errorf("signMsg not deterministic: %q != %q", result, result2)
	}

	// Different inputs should produce different output
	result3 := signMsg(appID, "different", token)
	if result == result3 {
		t.Error("signMsg should differ for different messages")
	}
}

func TestSortDict(t *testing.T) {
	payload := map[string]interface{}{
		"z_key": "last",
		"a_key": "first",
		"m_key": 42,
	}

	got := sortDict(payload)
	want := `{"a_key":"first","m_key":42,"z_key":"last"}`
	if got != want {
		t.Errorf("sortDict() = %q, want %q", got, want)
	}
}

func TestSortDict_Empty(t *testing.T) {
	got := sortDict(map[string]interface{}{})
	if got != "{}" {
		t.Errorf("sortDict(empty) = %q, want {}", got)
	}
}

func TestCredentials_IsSet(t *testing.T) {
	c := Credentials{}
	if c.IsSet() {
		t.Error("empty credentials should not be set")
	}

	c = Credentials{Email: "a", Password: "b", APIID: "c", APIKey: "d"}
	if !c.IsSet() {
		t.Error("full credentials should be set")
	}

	c = Credentials{Email: "a", Password: "b"}
	if c.IsSet() {
		t.Error("partial credentials should not be set")
	}
}

func TestGeneratePhoneID(t *testing.T) {
	id := generatePhoneID()
	if len(id) == 0 {
		t.Error("generatePhoneID should return non-empty string")
	}
	// Should be UUID-like format
	if len(id) != 36 {
		t.Errorf("generatePhoneID length = %d, want 36 (UUID format)", len(id))
	}

	// Two calls should produce different IDs
	id2 := generatePhoneID()
	if id == id2 {
		t.Error("generatePhoneID should produce unique IDs")
	}
}
