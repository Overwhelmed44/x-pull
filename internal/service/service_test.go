package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/x-pull/x-pull/internal/config"
	"github.com/x-pull/x-pull/internal/reconcile"
	"github.com/x-pull/x-pull/internal/user"
)

type testSource struct{}

func (testSource) Fetch(ctx context.Context, s config.Source) ([]user.User, error) {
	if s.URL == "fail" {
		return nil, errors.New("HTTP failure")
	}
	return []user.User{{Email: "a", Password: "secret"}}, nil
}

type testSyncer struct {
	mu   sync.Mutex
	tags []string
}

func (s *testSyncer) Sync(ctx context.Context, target user.Target, users []user.User, allow bool) (reconcile.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tags = append(s.tags, target.Tag)
	return reconcile.Result{Added: 1}, nil
}

func TestFailedSourceDoesNotBlockOtherInbound(t *testing.T) {
	c := config.Config{Workers: 2, Inbounds: []config.Inbound{{Tag: "bad", Source: config.Source{URL: "fail"}}, {Tag: "good", Source: config.Source{URL: "ok"}}}}
	syncer := &testSyncer{}
	s := New(c, testSource{}, syncer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Once(context.Background()); err == nil {
		t.Fatal("expected one-shot failure")
	}
	if len(syncer.tags) != 1 || syncer.tags[0] != "good" {
		t.Fatal("incorrect inbound isolation")
	}
}

type blockingSource struct {
	started     chan struct{}
	mu          sync.Mutex
	active, max int
}

func (s *blockingSource) Fetch(ctx context.Context, _ config.Source) ([]user.User, error) {
	s.mu.Lock()
	s.active++
	s.max = max(s.max, s.active)
	s.mu.Unlock()
	s.started <- struct{}{}
	<-ctx.Done()
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return nil, ctx.Err()
}

func TestWorkerBoundAndShutdown(t *testing.T) {
	c := config.Config{Workers: 2, PollInterval: config.Duration(time.Hour), Inbounds: []config.Inbound{{Tag: "a"}, {Tag: "b"}, {Tag: "c"}}}
	source := &blockingSource{started: make(chan struct{}, 3)}
	s := New(c, source, &testSyncer{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for range 2 {
		select {
		case <-source.started:
		case <-time.After(time.Second):
			t.Fatal("workers not started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel requests")
	}
	if source.max != 2 || source.active != 0 {
		t.Fatal("worker bound or shutdown failed")
	}
}
