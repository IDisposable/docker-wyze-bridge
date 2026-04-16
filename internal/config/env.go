package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// env returns the value of an environment variable, or the default.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool returns a bool from an environment variable.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off", "none":
		return false
	}
	return fallback
}

// envInt returns an int from an environment variable.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// envFloat returns a float64 from an environment variable.
func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return fallback
	}
	return f
}

// envList returns a comma-separated env var as a trimmed, uppercased slice.
func envList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, strings.ToUpper(p))
		}
	}
	return result
}

// envDuration parses a duration string like "60s", "5m", "72h", "7d".
// Supports a "d" suffix for days (not in stdlib).
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	v = strings.TrimSpace(v)

	// Handle "Nd" (days) suffix
	if strings.HasSuffix(v, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err == nil {
			return time.Duration(days) * 24 * time.Hour
		}
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		// Try as plain integer seconds
		if secs, err2 := strconv.Atoi(v); err2 == nil {
			return time.Duration(secs) * time.Second
		}
		return fallback
	}
	return d
}
