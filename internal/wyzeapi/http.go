package wyzeapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// WyzeAPIError represents an error response from the Wyze API.
type WyzeAPIError struct {
	Code    string
	Message string
	Method  string
	Path    string
}

func (e *WyzeAPIError) Error() string {
	hint := wyzeErrorHint(e.Code)
	if hint != "" {
		return fmt.Sprintf("wyze api error: code=%s msg=%s method=%s path=%s — %s", e.Code, e.Message, e.Method, e.Path, hint)
	}
	return fmt.Sprintf("wyze api error: code=%s msg=%s method=%s path=%s", e.Code, e.Message, e.Method, e.Path)
}

// wyzeErrorHint maps known Wyze API response codes to actionable
// guidance for the operator. Returns "" when the code isn't one
// we recognise; the caller renders the raw error in that case.
func wyzeErrorHint(code string) string {
	switch code {
	case "1000":
		return "Wyze cloud internal error; usually transient. Retry."
	case "1001":
		return "Bad credentials. Verify WYZE_EMAIL / WYZE_PASSWORD; confirm the account isn't locked."
	case "1003":
		return "Invalid request signature. Likely a stale or wrong WYZE_API_ID / WYZE_API_KEY pair from the Wyze developer console."
	case "1004":
		return "Sign-in restriction (geo or device limit). Try again from a previously-used device IP."
	case "2001":
		return "Access token expired. Bridge will re-login automatically."
	case "3019":
		return "TOTP / MFA required or wrong. Set WYZE_TOTP_KEY to the base32 secret from Wyze's authenticator-app enrollment screen."
	case "3024":
		return "Account is locked or password-changed. Reset via the Wyze app, then update WYZE_PASSWORD."
	}
	return ""
}

// classifyNetworkError wraps a net/http transport error with a hint
// that distinguishes DNS failures, timeouts, connection refused,
// etc. so the operator can act on what they see in the log.
func classifyNetworkError(err error) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("Wyze cloud DNS lookup failed (%v) — check network / DNS resolver", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("Wyze cloud request timed out — cloud may be slow or unreachable: %w", err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("Wyze cloud network error (%s) — check connectivity to api.wyzecam.com: %w", opErr.Op, err)
	}
	return fmt.Errorf("Wyze cloud request failed: %w", err)
}

// AccessTokenExpired returns true if this error indicates an expired token.
func (e *WyzeAPIError) AccessTokenExpired() bool {
	return e.Code == "2001"
}

// RateLimitError represents a Wyze API rate limit response.
type RateLimitError struct {
	Remaining int
	ResetBy   string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited: %d remaining, reset by %s", e.Remaining, e.ResetBy)
}

// postJSON sends a JSON POST request and validates the response.
func (c *Client) postJSON(url string, headers map[string]string, body map[string]interface{}) (map[string]interface{}, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	c.log.Trace().Str("url", url).RawJSON("body", jsonBody).Interface("headers", headers).Msg("POST (json)")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.record(url, 0, time.Since(start), true)
		}
		return nil, classifyNetworkError(err)
	}
	defer resp.Body.Close()

	result, vErr := c.validateResponse(resp)
	if c.metrics != nil {
		c.metrics.record(url, resp.StatusCode, time.Since(start), vErr != nil)
	}
	return result, vErr
}

// postRaw sends a raw string body POST request and validates the response.
func (c *Client) postRaw(url string, headers map[string]string, body string) (map[string]interface{}, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	c.log.Trace().Str("url", url).Msg("POST (raw)")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.metrics != nil {
			c.metrics.record(url, 0, time.Since(start), true)
		}
		return nil, classifyNetworkError(err)
	}
	defer resp.Body.Close()

	result, vErr := c.validateResponse(resp)
	if c.metrics != nil {
		c.metrics.record(url, resp.StatusCode, time.Since(start), vErr != nil)
	}
	return result, vErr
}

func (c *Client) validateResponse(resp *http.Response) (map[string]interface{}, error) {
	// Check rate limiting
	if remaining := resp.Header.Get("X-RateLimit-Limit"); remaining != "" {
		n, _ := strconv.Atoi(remaining)
		if n > 0 && n <= 10 {
			return nil, &RateLimitError{
				Remaining: n,
				ResetBy:   resp.Header.Get("X-RateLimit-Reset-By"),
			}
		}
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	c.log.Trace().
		Int("status", resp.StatusCode).
		Str("path", resp.Request.URL.Path).
		Str("body", string(bodyBytes)).
		Msg("response")

	var result map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("json decode (status %d): %w", resp.StatusCode, err)
	}

	// Check response code
	respCode := "0"
	if code, ok := result["code"]; ok {
		respCode = fmt.Sprintf("%v", code)
	} else if code, ok := result["errorCode"]; ok {
		respCode = fmt.Sprintf("%v", code)
	}

	if respCode == "2001" {
		return nil, &WyzeAPIError{
			Code:    "2001",
			Message: "access token expired",
			Method:  "POST",
			Path:    resp.Request.URL.Path,
		}
	}

	if respCode != "1" && respCode != "0" {
		msg := ""
		if m, ok := result["msg"].(string); ok {
			msg = m
		} else if m, ok := result["description"].(string); ok {
			msg = m
		}
		return nil, &WyzeAPIError{
			Code:    respCode,
			Message: msg,
			Method:  "POST",
			Path:    resp.Request.URL.Path,
		}
	}

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("Wyze cloud HTTP %d (%s) — cloud is degraded, will retry", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("Wyze cloud HTTP 429 (rate limited) — back off and retry")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Wyze cloud HTTP %d (%s) — check WYZE_API_ID / WYZE_API_KEY, account may be locked", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Wyze cloud HTTP %d (%s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), string(bodyBytes))
	}

	// Return the "data" sub-object if present, otherwise the whole thing
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}
