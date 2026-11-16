package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/server"
)

func main() {
	path := flag.String("config", "configs/cloudlet.json", "configuration file")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(*path)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, cfg, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
