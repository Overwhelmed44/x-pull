// Package service schedules bounded, non-overlapping synchronization cycles.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/x-pull/x-pull/internal/config"
	"github.com/x-pull/x-pull/internal/reconcile"
	"github.com/x-pull/x-pull/internal/user"
)

type Source interface {
	Fetch(context.Context, config.Source) ([]user.User, error)
}
type Syncer interface {
	Sync(context.Context, user.Target, []user.User, bool) (reconcile.Result, error)
}

type Service struct {
	config config.Config
	source Source
	syncer Syncer
	log    *slog.Logger
}

func New(c config.Config, source Source, syncer Syncer, log *slog.Logger) *Service {
	return &Service{config: c, source: source, syncer: syncer, log: log}
}

// Run polls immediately, then waits poll_interval after each completed cycle.
// Operational failures are retried on the next cycle; cancellation stops work.
func (s *Service) Run(ctx context.Context) {
	for ctx.Err() == nil {
		_ = s.Once(ctx)
		timer := time.NewTimer(time.Duration(s.config.PollInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) Once(ctx context.Context) error {
	jobs := make(chan config.Inbound)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	workers := min(s.config.Workers, len(s.config.Inbounds))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for inbound := range jobs {
				if ctx.Err() != nil {
					return
				}
				start := time.Now()
				users, err := s.source.Fetch(ctx, inbound.Source)
				var result reconcile.Result
				if err == nil {
					result, err = s.syncer.Sync(ctx, inbound.Target(), users, inbound.AllowEmpty)
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Errorf("inbound %q: %w", inbound.Tag, err))
					mu.Unlock()
					if ctx.Err() == nil {
						s.log.Error("sync failed", "inbound", inbound.Tag, "error", err, "added", result.Added, "updated", result.Updated, "removed", result.Removed)
					}
				} else {
					s.log.Info("sync complete", "inbound", inbound.Tag, "users", len(users), "added", result.Added, "updated", result.Updated, "removed", result.Removed, "duration", time.Since(start))
				}
			}
		}()
	}
dispatch:
	for _, inbound := range s.config.Inbounds {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- inbound:
		}
	}
	close(jobs)
	wg.Wait()
	return errors.Join(append(failures, ctx.Err())...)
}
