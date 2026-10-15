package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	providers := server.ProviderFactories{
		Source: func() (companion.Source, error) { return companion.NewWeRead(os.Getenv("WEREAD_API_KEY")) },
		Target: func() (companion.Target, error) { return companion.NewHardcover(os.Getenv("HARDCOVER_API_KEY")) },
	}
	if err := server.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, providers); err != nil {
		fmt.Fprintln(os.Stderr, "drifter-server:", err)
		os.Exit(1)
	}
}
