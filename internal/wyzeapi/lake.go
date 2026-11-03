package wyzeapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// LakeSession is a short-lived, camera-scoped bootstrap for the integrated
// WebRTC gateway. It must never be logged or exposed outside the Bridge process.
type LakeSession struct {
	AppID          string `json:"app_id"`
	Channel        string `json:"channel"`
	UID            int    `json:"uid"`
	RTCToken       string `json:"rtc_token"`
	UserID         string `json:"user_id"`
	EncryptionMode int    `json:"encryption_mode"`
	EncryptionKey  string `json:"encryption_key"`
	EncryptionSalt string `json:"encryption_salt"`
	Lifetime       int    `json:"lifetime_seconds"`
}

func (c *Client) RenewLakeToken(ctx context.Context, mac, model string, uid int) (string, error) {
	if model != "HL_PAN4" || uid < 10000 || uid > 99999 {
		return "", fmt.Errorf("invalid Lake renewal request")
	}
	resp, err := c.withLakeAuth(func(token, _ string) (map[string]interface{}, error) {
		return c.lakePost(ctx, "/v4/wcsa/renew-token", map[string]interface{}{
			"device_id": mac, "device_model": model, "uid": uid,
			"expire_time": 3600, "nonce": time.Now().UnixMilli(),
		}, "wyze_web_3.3.5", token)
	})
	if err != nil {
		return "", fmt.Errorf("Lake token renewal failed")
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok || getString(data, "rtc_token") == "" {
		return "", fmt.Errorf("Lake renewal response is incomplete")
	}
	return getString(data, "rtc_token"), nil
}

// GetLakeSession reuses Bridge authentication, including its refresh/re-login
// handling. The receiver never needs an account password or a state-file mount.
func (c *Client) GetLakeSession(ctx context.Context, mac, model string) (*LakeSession, error) {
	if model != "HL_PAN4" {
		return nil, fmt.Errorf("Lake receiver is currently supported for Pan v4 only")
	}
	randomUID, err := rand.Int(rand.Reader, big.NewInt(90000))
	if err != nil {
		return nil, fmt.Errorf("Lake UID generation failed")
	}
	uid := int(randomUID.Int64()) + 10000
	var token, userID string
	resp, err := c.withLakeAuth(func(accessToken, accountID string) (map[string]interface{}, error) {
		token, userID = accessToken, accountID
		streams, err := c.lakePost(ctx, "/v4/camera/get-streams", map[string]interface{}{
			"device_list": []interface{}{map[string]interface{}{
				"device_id": mac, "device_model": model, "provider": "lake",
				"parameters": map[string]interface{}{"use_trickle": true},
			}}, "nonce": time.Now().UnixMilli(),
		}, "wyze_web_2.3.1", token)
		if err != nil {
			return nil, err
		}
		data, ok := streams["data"].([]interface{})
		if !ok || len(data) != 1 {
			return nil, fmt.Errorf("Lake stream response is incomplete")
		}
		stream, ok := data[0].(map[string]interface{})
		if !ok || getString(stream, "provider") != "lake" {
			return nil, fmt.Errorf("camera does not provide a Lake stream")
		}
		params, ok := stream["params"].(map[string]interface{})
		if !ok || getInt(params, "encryption_mode") != 7 {
			return nil, fmt.Errorf("unsupported Lake media encryption")
		}
		connection, err := c.lakePost(ctx, "/v4/wcsa/create-connection", map[string]interface{}{
			"device_id": mac, "device_model": model, "mode": 3, "uid": uid,
			"expire_time": 3600, "resolution": 2, "nonce": time.Now().UnixMilli(),
		}, "wyze_web_3.3.5", token)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"params": params, "connection": connection["data"]}, nil
	})
	if err != nil {
		// Authentication/HTTP errors can contain upstream messages. Do not
		// pass them to the local receiver or log session provisioning data.
		return nil, fmt.Errorf("Lake provisioning failed; check Bridge authentication and camera availability")
	}
	params := resp["params"].(map[string]interface{})
	connection, ok := resp["connection"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Lake connection response is incomplete")
	}
	key, err := unwrapLakeKey(getString(params, "encryption_key"), token)
	if err != nil {
		return nil, err
	}
	salt, err := unwrapLakeKey(getString(params, "encryption_salt"), token)
	if err != nil {
		return nil, err
	}
	saltBytes, err := base64.StdEncoding.DecodeString(salt)
	if err != nil || len(saltBytes) != 32 {
		return nil, fmt.Errorf("invalid Lake media salt")
	}
	s := &LakeSession{
		AppID: getString(connection, "app_id"), Channel: getString(params, "channel"),
		UID: getInt(connection, "uid"), RTCToken: getString(connection, "rtc_token"), UserID: userID,
		EncryptionMode: 7, EncryptionKey: key, EncryptionSalt: salt, Lifetime: 3600,
	}
	if s.AppID == "" || s.Channel == "" || s.RTCToken == "" || s.UserID == "" || s.UID <= 0 || key == "" {
		return nil, fmt.Errorf("Lake session response is incomplete")
	}
	return s, nil
}

// Use the existing one-relogin retry policy, but take a consistent auth
// snapshot under the refresh lock. Network requests must not hold that lock.
func (c *Client) withLakeAuth(call func(token, userID string) (map[string]interface{}, error)) (map[string]interface{}, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, userID, err := c.lakeAuthSnapshot()
		if err != nil {
			return nil, err
		}
		resp, err := call(token, userID)
		if !isAccessTokenExpired(err) || attempt == 1 {
			return resp, err
		}
		c.log.Warn().Msg("Wyze rejected the Lake access token; logging in again")
		if err := c.forceRelogin(token); err != nil {
			return nil, fmt.Errorf("Lake authentication retry failed")
		}
	}
	return nil, fmt.Errorf("Lake authentication failed")
}

func (c *Client) lakeAuthSnapshot() (string, string, error) {
	c.reloginMu.Lock()
	defer c.reloginMu.Unlock()
	if err := c.ensureAuthLocked(); err != nil {
		return "", "", err
	}
	if c.auth == nil || c.auth.AccessToken == "" {
		return "", "", fmt.Errorf("Lake authentication unavailable")
	}
	return c.auth.AccessToken, c.auth.UserID, nil
}

// The signing constant is part of Wyze's public web client, not an account
// credential. Unlike postJSON/validateResponse, this path never traces bodies.
func (c *Client) lakePost(ctx context.Context, path string, payload map[string]interface{}, appInfo, token string) (map[string]interface{}, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("Lake request encoding failed")
	}
	digest := md5.Sum([]byte(token + "gbJojEBViLklgwyyDikx5ztSvKBXI5oU"))
	mac := hmac.New(md5.New, []byte(hex.EncodeToString(digest[:])))
	_, _ = mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.NewWyzeURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("Lake request creation failed")
	}
	for k, v := range map[string]string{
		"Content-Type": "application/json", "appid": "strv_e7f78e9e7738dc50", "appinfo": appInfo,
		"signature2": hex.EncodeToString(mac.Sum(nil)), "access_token": token, "Authorization": token,
		"User-Agent": "Mozilla/5.0", "Origin": "https://my.wyze.com", "Referer": "https://my.wyze.com/",
	} {
		req.Header.Set(k, v)
	}
	start := time.Now()
	client := *c.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	r, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Lake request failed")
	}
	defer r.Body.Close()
	var result map[string]interface{}
	err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&result)
	if c.metrics != nil {
		c.metrics.record(c.NewWyzeURL+path, r.StatusCode, time.Since(start), err != nil || r.StatusCode != http.StatusOK)
	}
	if err != nil || r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Lake response failed")
	}
	switch fmt.Sprint(result["code"]) {
	case "1":
		return result, nil
	case "2001":
		return nil, &WyzeAPIError{Code: "2001", Message: "access token expired"}
	default:
		return nil, fmt.Errorf("Lake API rejected the request")
	}
}

// Wyze wraps media credentials with XXTEA using the access token's first
// 16 bytes. Work on uint32 words so the algorithm's wraparound is explicit.
func unwrapLakeKey(encoded, token string) (string, error) {
	invalid := fmt.Errorf("invalid wrapped Lake media credential")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 8 || len(raw)%4 != 0 || len(raw) > 4096 {
		return "", invalid
	}
	v := make([]uint32, len(raw)/4)
	for i := range v {
		v[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	var key [16]byte
	copy(key[:], token)
	var k [4]uint32
	for i := range k {
		k[i] = binary.LittleEndian.Uint32(key[i*4:])
	}
	const delta uint32 = 0x9e3779b9
	sum := uint32(6+52/len(v)) * delta
	y := v[0]
	for sum != 0 {
		e := (sum >> 2) & 3
		for p := len(v) - 1; p >= 0; p-- {
			z := v[(p+len(v)-1)%len(v)]
			mix := ((z>>5 ^ y<<2) + (y>>3 ^ z<<4)) ^ ((sum ^ y) + (k[(uint32(p)&3)^e] ^ z))
			v[p] -= mix
			y = v[p]
		}
		sum -= delta
	}
	n := int(v[len(v)-1])
	if n < len(raw)-7 || n > len(raw)-4 {
		return "", invalid
	}
	for i := range v {
		binary.LittleEndian.PutUint32(raw[i*4:], v[i])
	}
	return string(raw[:n]), nil
}
