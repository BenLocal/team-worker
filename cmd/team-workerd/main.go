// Command team-workerd is the daemon that owns the team-worker SQLite database
// and serves the API used by the team-worker CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/server"
)

func main() {
	dataDir := flag.String("data-dir", config.DataDir(), "Directory for the database, socket and log")
	socket := flag.String("socket", "", "Unix socket path (default: <data-dir>/team-workerd.sock)")
	flag.Parse()
	if *socket == "" {
		*socket = config.SocketPath(*dataDir)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := server.Run(ctx, server.Options{DataDir: *dataDir, Socket: *socket, Logger: logger}); err != nil {
		fmt.Fprintln(os.Stderr, "team-workerd:", err)
		os.Exit(1)
	}
}
