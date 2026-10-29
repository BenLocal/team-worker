// Command team-workerd is the daemon that owns the team-worker SQLite database
// and serves the API used by the team-worker CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/logging"
	"github.com/benlocal/team-worker/internal/server"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func main() {
	dataDir := flag.String("data-dir", config.DataDir(), "Directory for the database, socket and log")
	socket := flag.String("socket", "", "Unix socket path (default: <data-dir>/team-workerd.sock)")
	httpAddr := flag.String("listen", config.HTTPAddr(), "Optional HTTP listen address (env TEAM_WORKER_HTTP_ADDR); uses the shared SQLite access key")
	var logOpts logging.Options
	flag.StringVar(&logOpts.Level, "log-level", envOr("TEAM_WORKER_LOG_LEVEL", "info"),
		"Log level: debug, info, warn or error (env TEAM_WORKER_LOG_LEVEL)")
	flag.StringVar(&logOpts.Format, "log-format", envOr("TEAM_WORKER_LOG_FORMAT", "text"),
		"Log format: text or json (env TEAM_WORKER_LOG_FORMAT)")
	flag.StringVar(&logOpts.File, "log-file", os.Getenv("TEAM_WORKER_LOG_FILE"),
		"Write logs to this file with size rotation instead of stderr (env TEAM_WORKER_LOG_FILE)")
	flag.IntVar(&logOpts.MaxSizeMB, "log-max-size", envInt("TEAM_WORKER_LOG_MAX_SIZE", 20),
		"Rotate the log file at this many MB (env TEAM_WORKER_LOG_MAX_SIZE)")
	flag.IntVar(&logOpts.MaxFiles, "log-max-files", envInt("TEAM_WORKER_LOG_MAX_FILES", 5),
		"Rotated log files to keep (env TEAM_WORKER_LOG_MAX_FILES)")
	flag.Parse()
	if *socket == "" {
		*socket = config.SocketPath(*dataDir)
	}

	logger, closer, err := logging.New(logOpts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team-workerd:", err)
		os.Exit(2)
	}
	defer closer.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx, server.Options{DataDir: *dataDir, Socket: *socket, HTTPAddr: *httpAddr, APIKey: config.APIKey(), Logger: logger}); err != nil {
		logger.Error("team-workerd failed", "err", err)
		fmt.Fprintln(os.Stderr, "team-workerd:", err)
		closer.Close()
		os.Exit(1)
	}
}
