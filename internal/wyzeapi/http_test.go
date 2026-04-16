package wyzeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func TestClient_PostJSON_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{
				"result": "ok",
			},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
	}

	resp, err := c.postJSON(server.URL+"/test", map[string]string{"x-test": "1"}, map[string]interface{}{"key": "val"})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	if resp["result"] != "ok" {
		t.Errorf("result = %v", resp["result"])
	}
}

func TestClient_PostJSON_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1000",
			"msg":  "invalid credentials",
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
	}

	_, err := c.postJSON(server.URL+"/test", nil, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for code 1000")
	}
	apiErr, ok := err.(*WyzeAPIError)
	if !ok {
		t.Fatalf("expected WyzeAPIError, got %T: %v", err, err)
	}
	if apiErr.Code != "1000" {
		t.Errorf("code = %q, want 1000", apiErr.Code)
	}
}

func TestClient_PostJSON_TokenExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "2001",
			"msg":  "access token expired",
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
	}

	_, err := c.postJSON(server.URL+"/test", nil, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for code 2001")
	}
	apiErr, ok := err.(*WyzeAPIError)
	if !ok {
		t.Fatalf("expected WyzeAPIError, got %T", err)
	}
	if !apiErr.AccessTokenExpired() {
		t.Error("should be access token expired")
	}
}

func TestClient_PostJSON_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5")
		w.Header().Set("X-RateLimit-Reset-By", "Mon Apr 14 12:00:00 UTC 2026")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "1",
			"data": map[string]interface{}{},
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
	}

	_, err := c.postJSON(server.URL+"/test", nil, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	_, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
}

func TestClient_PostJSON_NoDataField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code":   "1",
			"result": "direct",
		})
	}))
	defer server.Close()

	c := &Client{
		log:        zerolog.Nop(),
		httpClient: server.Client(),
	}

	resp, err := c.postJSON(server.URL, nil, map[string]interface{}{})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	// When no "data" field, should return the whole response
	if resp["result"] != "direct" {
		t.Errorf("result = %v", resp["result"])
	}
}

func TestWyzeAPIError_String(t *testing.T) {
	err := &WyzeAPIError{Code: "1000", Message: "test", Method: "POST", Path: "/api/test"}
	s := err.Error()
	if s == "" {
		t.Error("error string should not be empty")
	}
}

func TestRateLimitError_String(t *testing.T) {
	err := &RateLimitError{Remaining: 3, ResetBy: "tomorrow"}
	s := err.Error()
	if s == "" {
		t.Error("error string should not be empty")
	}
}
