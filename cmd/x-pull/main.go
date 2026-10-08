package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/x-pull/x-pull/internal/poller"
)

func main() {
	path := flag.String("config", "/etc/x-pull/poller.json", "JSON config path")
	once := flag.Bool("once", false, "sync once and exit")
	flag.Parse()
	config, err := poller.LoadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	service, err := poller.New(config)
	if err != nil {
		log.Fatal(err)
	}
	defer service.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		if err := service.Once(ctx); err != nil {
			os.Exit(1)
		}
		return
	}
	service.Run(ctx)
}
