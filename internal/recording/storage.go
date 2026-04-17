package recording

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StorageStats is a snapshot of on-disk footprint for one stratum
// (recordings or snapshots). Populated by the background sampler; the
// metrics page reads the cached struct rather than walking the tree
// itself, so refreshing the page doesn't hit the disk.
type StorageStats struct {
	// Total bytes across the whole tree.
	TotalBytes int64
	// Per-camera bytes, keyed by normalized camera name. Cameras with
	// zero files won't appear here.
	PerCamera map[string]int64
	// LastRefresh is when the sampler last completed a walk.
	LastRefresh time.Time
	// RefreshDuration is how long the last walk took — useful for
	// detecting when recording volume has grown past what a 60s
	// sampler can keep up with.
	RefreshDuration time.Duration
}

// StorageSampler walks the recording path on a cadence and caches
// the total + per-camera byte counts. Cheap enough at bridge scales
// (a few hundred MP4 files) that 60s is plenty; the walk runs off
// the hot path entirely.
//
// Snapshots aren't sampled here — they're tiny JPEGs and a separate
// concern. Add a second sampler if/when someone asks.
type StorageSampler struct {
	recordingsRoot string // prefix before %Y/%m/%d templates
	interval       time.Duration

	mu    sync.RWMutex
	stats StorageStats
}

// NewStorageSampler creates a sampler for the given RECORD_PATH
// template. The template's strftime-free prefix is the walk root
// (e.g. "/media/recordings/{cam_name}/%Y/%m/%d" → "/media/recordings").
// Interval must be positive.
func NewStorageSampler(recordPathTemplate string, interval time.Duration) *StorageSampler {
	if interval < time.Second {
		interval = 60 * time.Second
	}
	return &StorageSampler{
		recordingsRoot: walkRoot(recordPathTemplate),
		interval:       interval,
		stats: StorageStats{
			PerCamera: map[string]int64{},
		},
	}
}

// Run samples once immediately, then every interval until ctx ends.
// Blocks; call from a goroutine.
func (s *StorageSampler) Run(ctx context.Context) {
	s.refresh()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refresh()
		}
	}
}

// Stats returns the most recent snapshot. Safe to call concurrently
// with Run.
func (s *StorageSampler) Stats() StorageStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StorageStats{
		TotalBytes:      s.stats.TotalBytes,
		PerCamera:       cloneMap(s.stats.PerCamera),
		LastRefresh:     s.stats.LastRefresh,
		RefreshDuration: s.stats.RefreshDuration,
	}
}

// TotalBytes satisfies the webui.StorageObserver interface.
func (s *StorageSampler) TotalBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats.TotalBytes
}

// PerCamera satisfies the webui.StorageObserver interface.
func (s *StorageSampler) PerCamera() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMap(s.stats.PerCamera)
}

// LastRefresh satisfies the webui.StorageObserver interface.
func (s *StorageSampler) LastRefresh() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats.LastRefresh
}

func (s *StorageSampler) refresh() {
	start := time.Now()
	total := int64(0)
	perCamera := map[string]int64{}

	// Camera name is the first path segment under the root, since our
	// default template is {cam_name}/%Y/%m/%d. Users who've reshaped
	// the template still get TotalBytes; per-camera may be empty.
	_ = filepath.Walk(s.recordingsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".mp4" {
			return nil
		}
		total += info.Size()

		rel, err := filepath.Rel(s.recordingsRoot, path)
		if err != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) >= 2 {
			perCamera[parts[0]] += info.Size()
		}
		return nil
	})

	dur := time.Since(start)
	s.mu.Lock()
	s.stats.TotalBytes = total
	s.stats.PerCamera = perCamera
	s.stats.LastRefresh = time.Now()
	s.stats.RefreshDuration = dur
	s.mu.Unlock()
}

// walkRoot returns the prefix of a path template before any strftime
// or {cam_name} substitution. "/media/recordings/{cam_name}/%Y/%m/%d"
// → "/media/recordings". Empty template returns an empty string
// (caller's walk will do nothing).
func walkRoot(template string) string {
	tokens := []string{"{", "%"}
	cut := len(template)
	for _, tok := range tokens {
		if i := strings.Index(template, tok); i >= 0 && i < cut {
			cut = i
		}
	}
	root := strings.TrimRight(template[:cut], "/")
	return root
}

func cloneMap(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
