package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/CicerBro/speedtest/internal/config"
	"github.com/CicerBro/speedtest/internal/server"
)

// runServe runs the server until SIGINT/SIGTERM. Exit code 0 on clean
// shutdown, 2 on configuration errors, 1 on runtime errors.
func runServe(args []string) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			config.Usage(os.Stdout)
			return 0
		}
		fmt.Fprintf(os.Stderr, "speedtest serve: configuration error:\n%v\n\nrun 'speedtest serve -h' for help\n", err)
		return 2
	}
	log := cfg.Logger(os.Stderr)

	srv, err := server.New(cfg, server.WithLogger(log), server.WithVersion(version))
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		log.Error("server stopped", "err", err)
		return 1
	}
	log.Info("shutdown complete")
	return 0
}
