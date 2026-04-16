package webui

import (
	"crypto/subtle"
	"net/http"
)

// AuthMiddleware provides Basic Auth and Bearer token authentication.
type AuthMiddleware struct {
	enabled  bool
	username string
	password string
	apiKey   string // for Bearer token access
}

// NewAuthMiddleware creates a new auth middleware.
func NewAuthMiddleware(enabled bool, username, password, apiKey string) *AuthMiddleware {
	return &AuthMiddleware{
		enabled:  enabled,
		username: username,
		password: password,
		apiKey:   apiKey,
	}
}

// Wrap wraps an HTTP handler with authentication.
func (a *AuthMiddleware) Wrap(handler http.HandlerFunc) http.HandlerFunc {
	if !a.enabled {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Check Bearer token first
		if a.apiKey != "" {
			bearer := r.Header.Get("Authorization")
			if len(bearer) > 7 && bearer[:7] == "Bearer " {
				token := bearer[7:]
				if subtle.ConstantTimeCompare([]byte(token), []byte(a.apiKey)) == 1 {
					handler(w, r)
					return
				}
			}
		}

		// Fall back to Basic Auth
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(a.username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(a.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Wyze Bridge"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		handler(w, r)
	}
}
