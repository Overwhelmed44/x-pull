// x-pull-container is the image's PID 1: it runs Xray and the HTTP poller.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/x-pull/x-pull/internal/config"
	"github.com/x-pull/x-pull/internal/supervisor"
)

func main() { os.Exit(run()) }

func run() int {
	xrayConfig := flag.String("xray-config", "/etc/x-pull/xray.json", "Xray JSON config")
	pollerConfig := flag.String("poller-config", "/etc/x-pull/poller.json", "HTTP poller JSON config")
	grace := flag.Duration("shutdown-grace", 8*time.Second, "time to stop children before SIGKILL")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if *grace <= 0 || flag.NArg() != 0 {
		log.Error("invalid launcher arguments")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if _, err := config.Load(*pollerConfig); err != nil {
		log.Error("poller configuration failed", "error", err)
		return 1
	}
	// Validate Xray before starting either long-running child.
	validationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	check := exec.CommandContext(validationCtx, "/usr/local/bin/xray", "run", "-test", "-config", *xrayConfig)
	check.Stdout, check.Stderr = os.Stdout, os.Stderr
	err := check.Run()
	cancel()
	if ctx.Err() != nil {
		return 0
	}
	if err != nil {
		log.Error("Xray configuration validation failed", "error", err)
		return 1
	}
	err = supervisor.Run(ctx, []supervisor.Process{
		{Name: "xray", Command: "/usr/local/bin/xray", Args: []string{"run", "-config", *xrayConfig}},
		{Name: "poller", Command: "/usr/local/bin/x-pull", Args: []string{"-config", *pollerConfig}},
	}, *grace, log)
	if err != nil {
		log.Error("container failed", "error", err)
		return 1
	}
	log.Info("container stopped")
	return 0
}
