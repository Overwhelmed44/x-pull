package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/x-pull/x-pull/internal/config"
	"github.com/x-pull/x-pull/internal/reconcile"
	"github.com/x-pull/x-pull/internal/service"
	"github.com/x-pull/x-pull/internal/source"
	"github.com/x-pull/x-pull/internal/xray"
)

func main() { os.Exit(run()) }

func run() int {
	path := flag.String("config", "config.json", "path to JSON configuration")
	once := flag.Bool("once", false, "synchronize once and exit (nonzero on failure)")
	check := flag.Bool("check", false, "validate configuration and exit without network calls")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	c, err := config.Load(*path)
	if err != nil {
		log.Error("configuration failed", "error", err)
		return 1
	}
	if *check {
		fmt.Println("configuration valid")
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend, err := xray.New(c.Xray.Address, time.Duration(c.Xray.Timeout), int(c.HTTP.MaxResponseBytes)*2)
	if err != nil {
		log.Error("Xray client failed", "error", err)
		return 1
	}
	defer backend.Close()
	fetcher := source.New(time.Duration(c.HTTP.Timeout), c.HTTP.MaxResponseBytes)
	defer fetcher.Close()
	s := service.New(c, fetcher, reconcile.New(backend), log)
	if *once {
		if err := s.Once(ctx); err != nil {
			log.Error("synchronization failed", "error", err)
			return 1
		}
		return 0
	}
	log.Info("service started", "inbounds", len(c.Inbounds), "poll_interval", time.Duration(c.PollInterval).String())
	s.Run(ctx)
	log.Info("service stopped")
	return 0
}
