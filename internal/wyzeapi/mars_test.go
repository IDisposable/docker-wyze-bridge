package wyzeapi

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestMD5HexOf(t *testing.T) {
	// Known vector: md5("hello") = 5d41402abc4b2a76b9719d911017c592
	if got := md5HexOf("hello"); got != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("md5HexOf(hello) = %q", got)
	}
	// Empty-string vector.
	if got := md5HexOf(""); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("md5HexOf('') = %q", got)
	}
}

// TestMarsSignBody_MatchesPython cross-checks our HMAC-MD5 against the
// canonical Python wyze-sdk algorithm:
//
//	key  = md5_hex(access_token + signing_secret).encode()
//	sig  = hmac.new(key, body, md5).hexdigest()
//
// We reimplement the reference in-line (identical to marsSignBody,
// but spelled out step-by-step) and assert bit-for-bit equality on
// several inputs. If anyone tweaks marsSignBody and forgets that the
// HMAC key is the HEX STRING of the MD5 (not the raw 16 bytes), the
// test fires.
func TestMarsSignBody_MatchesPython(t *testing.T) {
	cases := []struct {
		name, token, body string
	}{
		{"simple", "abc123", `{"nonce":"1","ttl_minutes":10080,"unique_id":"x"}`},
		{"empty_body", "token", ""},
		{"realistic", "eyJhbGci.eyJzdWIi.Rd5", `{"nonce":"1699999999999","ttl_minutes":10080,"unique_id":"abc-def"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reference reimplementation (should match marsSignBody exactly).
			keySum := md5.Sum([]byte(tc.token + marsSigningSecret))
			keyHex := hex.EncodeToString(keySum[:])
			m := hmac.New(md5.New, []byte(keyHex))
			m.Write([]byte(tc.body))
			want := hex.EncodeToString(m.Sum(nil))

			got := marsSignBody(tc.token, []byte(tc.body))
			if got != want {
				t.Errorf("sig mismatch\ngot  %s\nwant %s", got, want)
			}
			// Sanity: lowercase hex, 32 chars (MD5 output size).
			if len(got) != 32 {
				t.Errorf("signature length = %d, want 32", len(got))
			}
			if strings.ToLower(got) != got {
				t.Errorf("signature not lowercase: %q", got)
			}
		})
	}
}

// TestMarsRegisterGWUser_HappyPath spins up a local HTTP mock of the
// Mars endpoint, confirms we hit the right URL with the right headers
// and a valid signature, and that we parse the envelope correctly.
func TestMarsRegisterGWUser_HappyPath(t *testing.T) {
	var gotReq *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		// Echo back a success envelope.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(marsEnvelope{
			Code: 1,
			Msg:  "ok",
			Data: marsAccessCredential{
				AccessID:    "42",
				AccessToken: strings.Repeat("a", 128), // 64-byte hex
			},
		})
	}))
	defer srv.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: srv.Client(),
		auth:       &AuthState{AccessToken: "tok", PhoneID: "phone-xyz"},
	}

	// Redirect our call to the test server. Easiest way: override the
	// URL via reflection-lite — we can't swap the const so we call the
	// internals directly. Simpler path: reuse the real function but
	// temporarily swap its HTTP transport to rewrite the host.
	c.httpClient = &http.Client{
		Transport: rewriteHostTransport{target: srv.URL, next: http.DefaultTransport},
	}

	accessID, accessToken, err := c.MarsRegisterGWUser(context.Background(), "AABBCCDDEEFF")
	if err != nil {
		t.Fatalf("MarsRegisterGWUser: %v", err)
	}
	if accessID != "42" || !strings.HasPrefix(accessToken, "aaa") {
		t.Errorf("wrong creds returned: id=%q token=%q", accessID, accessToken)
	}

	// URL path — must end with the device ID.
	if !strings.HasSuffix(gotReq.URL.Path, "/plugin/mars/v2/regist_gw_user/AABBCCDDEEFF") {
		t.Errorf("request path = %q", gotReq.URL.Path)
	}

	// Method is POST.
	if gotReq.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", gotReq.Method)
	}

	// Required headers.
	for _, h := range []string{"access_token", "requestid", "signature2", "appid"} {
		if gotReq.Header.Get(h) == "" {
			t.Errorf("missing header %q", h)
		}
	}
	if gotReq.Header.Get("access_token") != "tok" {
		t.Errorf("access_token header = %q", gotReq.Header.Get("access_token"))
	}
	if gotReq.Header.Get("appid") != marsAppID {
		t.Errorf("appid header = %q", gotReq.Header.Get("appid"))
	}

	// Signature must validate against the body bytes.
	wantSig := marsSignBody("tok", gotBody)
	if gotReq.Header.Get("signature2") != wantSig {
		t.Errorf("signature mismatch\ngot  %s\nwant %s", gotReq.Header.Get("signature2"), wantSig)
	}

	// Body must include ttl_minutes + unique_id + a nonce.
	var parsed map[string]interface{}
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, gotBody)
	}
	if parsed["ttl_minutes"].(float64) != float64(marsTTLMinutes) {
		t.Errorf("ttl_minutes = %v", parsed["ttl_minutes"])
	}
	if parsed["unique_id"].(string) != "phone-xyz" {
		t.Errorf("unique_id = %v", parsed["unique_id"])
	}
	if parsed["nonce"].(string) == "" {
		t.Error("nonce missing from body")
	}
}

func TestMarsRegisterGWUser_NoAuth(t *testing.T) {
	c := &Client{log: zerolog.Nop(), httpClient: http.DefaultClient}
	_, _, err := c.MarsRegisterGWUser(context.Background(), "AABBCCDDEEFF")
	if err == nil || !strings.Contains(err.Error(), "no wyze auth") {
		t.Errorf("expected no-auth error, got %v", err)
	}
}

func TestMarsRegisterGWUser_WyzeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(marsEnvelope{
			Code: 2001,
			Msg:  "invalid device",
		})
	}))
	defer srv.Close()

	c := &Client{
		log: zerolog.Nop(),
		httpClient: &http.Client{
			Transport: rewriteHostTransport{target: srv.URL, next: http.DefaultTransport},
		},
		auth: &AuthState{AccessToken: "tok", PhoneID: "phone"},
	}

	_, _, err := c.MarsRegisterGWUser(context.Background(), "DEADBEEFDEAD")
	if err == nil || !strings.Contains(err.Error(), "code=2001") {
		t.Errorf("expected wyze error, got %v", err)
	}
}

// rewriteHostTransport rewrites the scheme+host of outbound requests
// so we can point the Mars call at an httptest server without having
// to make the base URL configurable on Client. Test-only.
type rewriteHostTransport struct {
	target string // e.g. "http://127.0.0.1:12345"
	next   http.RoundTripper
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	target, err := parseURLScheme(t.target)
	if err != nil {
		return nil, err
	}
	clone.URL.Scheme = target.scheme
	clone.URL.Host = target.host
	clone.Host = target.host
	return t.next.RoundTrip(clone)
}

type schemeHost struct{ scheme, host string }

func parseURLScheme(u string) (schemeHost, error) {
	parts := strings.SplitN(u, "://", 2)
	if len(parts) != 2 {
		return schemeHost{}, &url2Err{u}
	}
	return schemeHost{parts[0], parts[1]}, nil
}

type url2Err struct{ u string }

func (e *url2Err) Error() string { return "bad url: " + e.u }
