package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
)

// Pruner removes old snapshot files.
type Pruner struct {
	log    zerolog.Logger
	dir    string
	maxAge time.Duration
}

// NewPruner creates a new snapshot pruner.
func NewPruner(dir string, maxAge time.Duration, log zerolog.Logger) *Pruner {
	return &Pruner{
		log:    log,
		dir:    dir,
		maxAge: maxAge,
	}
}

// Run starts the pruning loop.
func (p *Pruner) Run(ctx context.Context) {
	if p.maxAge <= 0 {
		p.log.Debug().Msg("snapshot pruning disabled (keep=0)")
		return
	}

	p.log.Info().Dur("max_age", p.maxAge).Str("dir", p.dir).Msg("snapshot pruner started")

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.prune()
		}
	}
}

func (p *Pruner) prune() {
	cutoff := time.Now().Add(-p.maxAge)
	var deleted int
	var freedBytes int64

	err := filepath.Walk(p.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".jpg" && ext != ".jpeg" {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			freedBytes += info.Size()
			if err := os.Remove(path); err == nil {
				deleted++
				p.log.Debug().Str("file", path).Msg("pruned snapshot")
			}
		}
		return nil
	})

	if err != nil {
		p.log.Warn().Err(err).Msg("snapshot prune walk error")
	}

	if deleted > 0 {
		p.log.Info().Int("deleted", deleted).Int64("freed_bytes", freedBytes).Msg("snapshot pruning complete")
	}
}
