package wyzeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	return fmt.Sprintf("wyze api error: code=%s msg=%s method=%s path=%s", e.Code, e.Message, e.Method, e.Path)
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
		return nil, fmt.Errorf("http post: %w", err)
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
		return nil, fmt.Errorf("http post: %w", err)
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

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Return the "data" sub-object if present, otherwise the whole thing
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}
