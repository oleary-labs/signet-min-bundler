package mempool

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Pruner periodically expires stale pending ops and prunes old terminal ops.
type Pruner struct {
	repo         Repository
	pendingTtlMs int64
	retentionMs  int64
	log          *zap.Logger
}

// NewPruner creates a Pruner with the given TTL and retention settings.
func NewPruner(repo Repository, pendingTtlMs, retentionMs int64, log *zap.Logger) *Pruner {
	return &Pruner{
		repo:         repo,
		pendingTtlMs: pendingTtlMs,
		retentionMs:  retentionMs,
		log:          log,
	}
}

// Run starts the pruner loop, running every 5 minutes until ctx is cancelled.
func (p *Pruner) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *Pruner) tick() {
	cutoff := time.Now().UnixMilli() - p.pendingTtlMs
	expired, err := p.repo.MarkTtlExpired(cutoff)
	if err != nil {
		p.log.Error("ttl expiry failed", zap.Error(err))
	} else if expired > 0 {
		p.log.Info("expired pending ops", zap.Int("count", expired))
	}

	retentionCutoff := time.Now().UnixMilli() - p.retentionMs
	deleted, err := p.repo.PruneHistory(retentionCutoff)
	if err != nil {
		p.log.Error("history prune failed", zap.Error(err))
	} else if deleted > 0 {
		p.log.Info("pruned history", zap.Int("count", deleted))
	}
}
