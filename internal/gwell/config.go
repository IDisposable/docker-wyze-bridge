package gwell

import (
	"fmt"
	"os"
)

// Config holds settings for the gwell-proxy subprocess.
type Config struct {
	// Enabled is the master switch. When false, Gwell cameras are not
	// routed to the proxy (they fall back to the bridge's historical
	// "skip Gwell" behavior).
	Enabled bool

	// BinaryPath is the path to the gwell-proxy executable. If empty,
	// common locations and $PATH are searched.
	BinaryPath string

	// RTSPPort is the loopback TCP port the proxy publishes RTSP on.
	// Only exposed on 127.0.0.1, never outside the container.
	RTSPPort int

	// ControlPort is the loopback TCP port the proxy exposes its
	// control HTTP API on.
	ControlPort int

	// StateDir is the directory where the proxy caches its cloud
	// session token (valid ~7 days) and any per-camera scratch state.
	StateDir string

	// LogLevel mirrors the bridge log level. Empty means "inherit".
	LogLevel string
}

// DefaultConfig returns the default Gwell configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:     true,
		BinaryPath:  "",
		RTSPPort:    8564,
		ControlPort: 18564,
		StateDir:    "",
		LogLevel:    "",
	}
}

// Validate checks the config for obviously-bad values and returns the
// first problem.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.RTSPPort <= 0 || c.RTSPPort > 65535 {
		return fmt.Errorf("gwell: RTSPPort %d is out of range", c.RTSPPort)
	}
	if c.ControlPort <= 0 || c.ControlPort > 65535 {
		return fmt.Errorf("gwell: ControlPort %d is out of range", c.ControlPort)
	}
	if c.RTSPPort == c.ControlPort {
		return fmt.Errorf("gwell: RTSPPort and ControlPort must differ (both %d)", c.RTSPPort)
	}
	return nil
}

// ResolveBinary returns a usable path to the gwell-proxy binary, or
// "" and an error if none can be found. The search order is:
//  1. c.BinaryPath if non-empty and the file exists
//  2. ./gwell-proxy in the current working directory
//  3. ./gwell-proxy.exe (Windows dev)
//  4. /usr/local/bin/gwell-proxy
//  5. /usr/bin/gwell-proxy
//  6. Fall back to bare "gwell-proxy" and let the OS do $PATH lookup
func (c Config) ResolveBinary() (string, error) {
	candidates := make([]string, 0, 6)
	if c.BinaryPath != "" {
		candidates = append(candidates, c.BinaryPath)
	}
	candidates = append(candidates,
		"./gwell-proxy",
		"./gwell-proxy.exe",
		"/usr/local/bin/gwell-proxy",
		"/usr/bin/gwell-proxy",
	)

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Last resort: return the bare name so exec.LookPath can try $PATH.
	// The caller will get a clear exec error if that also fails.
	return "gwell-proxy", nil
}

// BaseURL returns the base URL for the proxy's control HTTP API.
func (c Config) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.ControlPort)
}

// RTSPURL returns the loopback rtsp:// URL for a given camera name.
// The proxy publishes each camera at /<normalized_name> under its
// configured RTSPPort.
func (c Config) RTSPURL(camName string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", c.RTSPPort, camName)
}
