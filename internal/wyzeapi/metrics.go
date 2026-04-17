package wyzeapi

import (
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// EndpointStats is the per-endpoint call summary the metrics page
// displays. Populated by the Client's internal recorder; every
// postJSON / postRaw / Mars-signed call passes through it.
type EndpointStats struct {
	Path       string        // normalized endpoint path (e.g. "/v2/home_page/get_object_list")
	Count      int           // total calls
	Errors     int           // count that returned an error (network, non-2xx, or wyze code != 0/1)
	LastStatus int           // HTTP status of the most recent call (0 if transport failed)
	LastLatency time.Duration // wall-clock duration of the most recent call
	TotalNanos int64         // sum of durations; AvgLatency = TotalNanos / Count
	FirstCall  time.Time
	LastCall   time.Time
}

// AvgLatency returns the mean latency across all recorded calls.
func (s EndpointStats) AvgLatency() time.Duration {
	if s.Count == 0 {
		return 0
	}
	return time.Duration(s.TotalNanos / int64(s.Count))
}

type apiMetrics struct {
	mu        sync.Mutex
	endpoints map[string]*EndpointStats
}

func newAPIMetrics() *apiMetrics {
	return &apiMetrics{endpoints: map[string]*EndpointStats{}}
}

// record attaches a single call result to the endpoint's stats. Called
// from postJSON / postRaw after the HTTP round-trip completes.
func (m *apiMetrics) record(rawURL string, status int, dur time.Duration, errored bool) {
	p := normalizePath(rawURL)
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.endpoints[p]
	if !ok {
		s = &EndpointStats{Path: p, FirstCall: now}
		m.endpoints[p] = s
	}
	s.Count++
	if errored {
		s.Errors++
	}
	s.LastStatus = status
	s.LastLatency = dur
	s.TotalNanos += dur.Nanoseconds()
	s.LastCall = now
}

// EndpointStats returns a snapshot of all recorded endpoints sorted by
// path for stable UI rendering.
func (c *Client) EndpointStats() []EndpointStats {
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()

	out := make([]EndpointStats, 0, len(c.metrics.endpoints))
	for _, s := range c.metrics.endpoints {
		out = append(out, *s)
	}
	// Stable alphabetical by path.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Path < out[i].Path {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// normalizePath extracts a grouping key from the URL. Drops the host
// and query string; keeps the path. Mars device paths collapse the
// device-ID segment so all cameras roll into one row.
func normalizePath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return raw
	}
	p := u.Path
	// Mars: /plugin/mars/v2/regist_gw_user/<deviceID> → collapse the last segment
	if strings.Contains(p, "/plugin/mars/v2/regist_gw_user/") {
		return "/plugin/mars/v2/regist_gw_user/<deviceID>"
	}
	return path.Clean(p)
}
