package config

import (
	"os"
	"strings"
)

// secret returns the value of an environment variable, falling back
// to reading from /run/secrets/{VAR_NAME} (Docker secrets support).
func secret(key string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimSpace(v)
	}

	// Try Docker secret (case-insensitive filename)
	for _, name := range []string{key, strings.ToLower(key)} {
		path := "/run/secrets/" + name
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}

	return ""
}

// secretWithAlias tries the primary key first, then the alias.
func secretWithAlias(primary, alias string) string {
	if v := secret(primary); v != "" {
		return v
	}
	return secret(alias)
}
