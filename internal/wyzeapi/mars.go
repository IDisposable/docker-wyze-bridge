package wyzeapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Wyze Mars service — used by the gwell-proxy subprocess (via the
// /internal/wyze/Camera/CameraToken shim) to mint the Gwell-scoped
// accessId/accessToken pair that gwell.ParseAccessToken expects.
//
// Mars is a distinct Wyze cloud service from the primary auth API.
// It accepts the standard Wyze access_token we already hold plus a
// Wpk-signed request body and returns a short-lived P2P credential.
//
// Wire contract reverse-engineered from wlatic/hacky-wyze-gwell's
// Python wyze-api (which wraps WpkNetServiceClient from wyze-sdk).
// Signing reference: wyze_sdk/signature/__init__.py.

const (
	// marsBaseURL is the cloud host for the Gwell credential service.
	marsBaseURL = "https://wyze-mars-service.wyzecam.com"

	// marsRegisterPath is the regist_gw_user endpoint. The camera's
	// device ID (MAC for Gwell models) is appended as a path segment.
	marsRegisterPath = "/plugin/mars/v2/regist_gw_user/"

	// marsAppID is the Wpk service's advertised app_id — the same one
	// the Wyze Android app sends. Baked in by wyze-sdk and picked up
	// by mrlt8/wyzecam + community projects for this service.
	marsAppID = "9319141212m2ik"

	// marsSigningSecret is the "salt" concatenated with the user's
	// access_token before MD5-hashing to produce the HMAC key. Same
	// constant as the official Wyze app.
	marsSigningSecret = "wyze_app_secret_key_132"

	// marsTTLMinutes is the requested credential lifetime (7 days in
	// minutes — upstream's Python uses this exact value and Wyze
	// accepts it; shorter values work too but 7d avoids churn).
	marsTTLMinutes = 10080

	// marsUserAgent is the okhttp identifier the Wyze Android app
	// sends. Wpk doesn't appear to gate on it but mismatched values
	// have been observed to trigger rate-limiting on other endpoints.
	marsUserAgent = "okhttp/4.7.2"
)

// marsAccessCredential is the inner payload Wyze returns inside its
// standard envelope. Fields are lowerCamelCase on the wire.
type marsAccessCredential struct {
	AccessID    string `json:"accessId"`
	AccessToken string `json:"accessToken"`
}

// marsEnvelope is the Wyze-wide JSON envelope: code/msg describe
// success, data carries the per-call payload.
type marsEnvelope struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data marsAccessCredential `json:"data"`
}

// MarsRegisterGWUser mints a fresh Gwell-scoped accessId + accessToken
// for the given Wyze device. The deviceID is the camera's MAC for
// Gwell-protocol models (GW_BE1/GC1/GC2/DBD) — the same value the
// gwell-proxy receives from the /Camera/CameraList shim endpoint.
//
// Requires a valid Wyze cloud auth state (call EnsureAuth first).
// Thread-safe; callers can invoke concurrently per-camera.
func (c *Client) MarsRegisterGWUser(ctx context.Context, deviceID string) (accessID, accessToken string, err error) {
	if c.auth == nil || c.auth.AccessToken == "" {
		return "", "", fmt.Errorf("mars: no wyze auth — call EnsureAuth first")
	}
	if deviceID == "" {
		return "", "", fmt.Errorf("mars: deviceID required")
	}

	// Nonce is current time in milliseconds (13 digits). Same value
	// goes into the request body AND is MD5-squared for requestid.
	nonce := strconv.FormatInt(time.Now().UnixMilli(), 10)

	// Body must be compact JSON (no spaces) because signing hashes
	// the exact bytes we send. Go's json.Marshal is already compact.
	// Use an ordered struct so field ordering is deterministic —
	// HMAC doesn't care about order but verifiers testing against
	// the Python reference will want matching bytes.
	body, err := json.Marshal(struct {
		Nonce       string `json:"nonce"`
		TTLMinutes  int    `json:"ttl_minutes"`
		UniqueID    string `json:"unique_id"`
	}{
		Nonce:      nonce,
		TTLMinutes: marsTTLMinutes,
		UniqueID:   c.auth.PhoneID,
	})
	if err != nil {
		return "", "", fmt.Errorf("mars: marshal body: %w", err)
	}

	sig := marsSignBody(c.auth.AccessToken, body)
	reqID := md5HexOf(md5HexOf(nonce))

	url := marsBaseURL + marsRegisterPath + deviceID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("mars: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", marsUserAgent)
	req.Header.Set("access_token", c.auth.AccessToken)
	req.Header.Set("requestid", reqID)
	req.Header.Set("signature2", sig)
	req.Header.Set("appid", marsAppID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("mars: http do: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("mars: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("mars: %d %s: %s", resp.StatusCode, resp.Status, string(raw))
	}

	var env marsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", "", fmt.Errorf("mars: decode envelope: %w (body=%s)", err, string(raw))
	}

	// Wyze envelopes conventionally use code=1 for success (not 0).
	// Check both zero-value and 1 to be safe against alternate
	// envelopes that nest success differently.
	if env.Code != 1 && env.Code != 0 {
		return "", "", fmt.Errorf("mars: wyze error code=%d msg=%q", env.Code, env.Msg)
	}
	if env.Data.AccessID == "" || env.Data.AccessToken == "" {
		return "", "", fmt.Errorf("mars: empty credentials in response (body=%s)", string(raw))
	}

	c.log.Debug().
		Str("device", deviceID).
		Int("ttl_minutes", marsTTLMinutes).
		Msg("minted gwell credential via mars")

	return env.Data.AccessID, env.Data.AccessToken, nil
}

// marsSignBody implements the Wpk "dynamic signature" HMAC.
// Reference (Python wyze-sdk signature/__init__.py):
//
//	encoded_secret = md5_hex_string(access_token + signing_secret).encode()
//	sig = hmac_md5(encoded_secret, body_bytes).hex()
//
// Two gotchas the Python spells out:
//  1. The HMAC KEY is the lowercase-hex STRING of the MD5, not the
//     raw 16-byte digest.
//  2. The body signed is the EXACT bytes placed on the wire (nonce
//     already injected, compact JSON, no trailing newline).
func marsSignBody(accessToken string, body []byte) string {
	keyHex := md5HexOf(accessToken + marsSigningSecret)
	m := hmac.New(md5.New, []byte(keyHex))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// md5HexOf returns the lowercase hex-string of MD5(s).
func md5HexOf(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
