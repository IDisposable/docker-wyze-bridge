package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddleware_Disabled(t *testing.T) {
	auth := NewAuthMiddleware(false, "user", "pass", "")
	handler := auth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("disabled auth should pass through, got %d", w.Code)
	}
}

func TestAuthMiddleware_BasicAuth(t *testing.T) {
	auth := NewAuthMiddleware(true, "admin", "secret", "")
	handler := auth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// No auth
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth should be 401, got %d", w.Code)
	}

	// Wrong creds
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong creds should be 401, got %d", w.Code)
	}

	// Correct creds
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct creds should be 200, got %d", w.Code)
	}
}

func TestAuthMiddleware_BearerToken(t *testing.T) {
	auth := NewAuthMiddleware(true, "admin", "secret", "my-api-key")
	handler := auth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Correct bearer
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer my-api-key")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct bearer should be 200, got %d", w.Code)
	}

	// Wrong bearer
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong bearer should be 401, got %d", w.Code)
	}
}
