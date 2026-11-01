package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/spf13/cobra"
)

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect the team-worker configuration"}
	cmd.AddCommand(&cobra.Command{Use: "health", Short: "Check the data directory, socket, daemon or remote server",
		Long: "Checks what the CLI depends on: in local mode the data directory, database and socket and\n" +
			"whether the daemon answers; in remote mode the server URL, API key and whether the server\n" +
			"answers. It never starts a daemon. Exits 1 when it finds a problem.",
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			h := a.checkHealth(cmd.Context())
			a.emit(h, func(w io.Writer) { printHealth(w, h) })
			if len(h.Problems) > 0 {
				return exitError{}
			}
			return nil
		}})
	return cmd
}

// health is the result of config health. Field names follow ClawTeam's
// `config health` where both have the same check.
type health struct {
	Mode             string  `json:"mode"`
	DataDir          string  `json:"data_dir,omitempty"`
	DataDirSource    string  `json:"data_dir_source,omitempty"`
	Exists           bool    `json:"exists"`
	Writable         bool    `json:"writable"`
	LatencyMS        float64 `json:"latency_ms"`
	Database         string  `json:"database,omitempty"`
	DatabaseWritable bool    `json:"database_writable"`
	Socket           string  `json:"socket,omitempty"`
	SocketMode       string  `json:"socket_mode,omitempty"`
	ServerURL        string  `json:"server_url,omitempty"`
	ServerURLSource  string  `json:"server_url_source,omitempty"`
	APIKeySet        bool    `json:"api_key_set"`
	// Daemon is running, not running or unreachable.
	Daemon     string   `json:"daemon"`
	TeamsCount int      `json:"teams_count"`
	User       string   `json:"user"`
	UserSource string   `json:"user_source"`
	Problems   []string `json:"problems"`
}

// healthTimeout bounds each probe of the daemon or server.
const healthTimeout = 10 * time.Second

func (a *app) checkHealth(ctx context.Context) health {
	h := health{Problems: []string{}}
	h.User, h.UserSource = config.IdentityFromEnv().User, source("TEAM_WORKER_USER", config.Load().User != "")
	if url := config.ServerURL(); url != "" {
		h.Mode, h.ServerURL = "remote", url
		h.ServerURLSource = source("TEAM_WORKER_SERVER_URL", config.Load().ServerURL != "")
		h.APIKeySet = config.APIKey() != ""
		if !h.APIKeySet {
			h.problem("TEAM_WORKER_API_KEY is not set; the server rejects requests without it")
		}
		a.probeDaemon(ctx, &h)
		return h
	}
	h.Mode = "local"
	dir := config.DataDir()
	h.DataDir, h.DataDirSource = dir, source("TEAM_WORKER_DATA_DIR", config.Load().DataDir != "")
	checkDataDir(&h, dir)
	h.Socket = config.SocketPath(dir)
	info, err := os.Stat(h.Socket)
	if errors.Is(err, fs.ErrNotExist) {
		// The next CLI command starts the daemon.
		h.Daemon = "not running"
		return h
	}
	if err != nil {
		h.problem("socket: %v", err)
		h.Daemon = "unreachable"
		return h
	}
	h.SocketMode = fmt.Sprintf("%04o", info.Mode().Perm())
	if info.Mode().Perm()&0o077 != 0 {
		h.problem("socket %s is open to other users (mode %s); expected 0600", h.Socket, h.SocketMode)
	}
	// Dial first: the client would start a daemon behind a stale socket.
	conn, err := net.DialTimeout("unix", h.Socket, 2*time.Second)
	if err != nil {
		h.problem("socket %s does not answer (%v); remove it if no daemon runs, and check team-workerd.out", h.Socket, err)
		h.Daemon = "unreachable"
		return h
	}
	if err := conn.Close(); err != nil {
		h.problem("socket: %v", err)
	}
	a.probeDaemon(ctx, &h)
	return h
}

// checkDataDir checks that the data directory takes writes and that the
// database, once created, can be opened for writing by this user.
func checkDataDir(h *health, dir string) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// The daemon creates it on its first start.
		return
	}
	if err != nil || !info.IsDir() {
		h.problem("data directory %s is not a readable directory: %v", dir, err)
		return
	}
	h.Exists = true
	probe := filepath.Join(dir, ".health-check")
	start := time.Now()
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		h.problem("data directory %s is not writable: %v", dir, err)
	} else {
		data, readErr := os.ReadFile(probe)
		h.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		removeErr := os.Remove(probe)
		h.Writable = readErr == nil && string(data) == "ok" && removeErr == nil
		if !h.Writable {
			h.problem("data directory %s does not keep what is written to it: %v", dir, errors.Join(readErr, removeErr))
		}
	}
	h.Database = config.DBPath(dir)
	f, err := os.OpenFile(h.Database, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		h.problem("database %s cannot be opened for writing (owned by another user?): %v", h.Database, err)
		return
	}
	h.DatabaseWritable = true
	if err := f.Close(); err != nil {
		h.problem("database: %v", err)
	}
}

// probeDaemon asks the daemon or remote server for its teams.
func (a *app) probeDaemon(ctx context.Context, h *health) {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	teams, err := a.client().ListTeams(ctx)
	if err != nil {
		h.Daemon = "unreachable"
		h.problem("listing teams failed: %v", err)
		return
	}
	h.Daemon, h.TeamsCount = "running", len(teams)
}

func (h *health) problem(format string, args ...any) {
	h.Problems = append(h.Problems, fmt.Sprintf(format, args...))
}

// source names where a setting came from: the environment, config.json or
// the default.
func source(env string, inFile bool) string {
	if os.Getenv(env) != "" {
		return "env"
	}
	if inFile {
		return "config"
	}
	return "default"
}

func printHealth(w io.Writer, h health) {
	yes := map[bool]string{true: "yes", false: "no"}
	fmt.Fprintf(w, "Mode: %s\n", h.Mode)
	if h.Mode == "remote" {
		fmt.Fprintf(w, "Server: %s (%s)\n  API key set: %s\n", h.ServerURL, h.ServerURLSource, yes[h.APIKeySet])
	} else {
		fmt.Fprintf(w, "Data directory: %s (%s)\n  Exists: %s\n", h.DataDir, h.DataDirSource, yes[h.Exists])
		if h.Exists {
			fmt.Fprintf(w, "  Writable: %s (%.2f ms)\n", yes[h.Writable], h.LatencyMS)
		}
		if h.Database != "" {
			fmt.Fprintf(w, "  Database: %s (writable: %s)\n", h.Database, yes[h.DatabaseWritable])
		}
		fmt.Fprintf(w, "Socket: %s", h.Socket)
		if h.SocketMode != "" {
			fmt.Fprintf(w, " (%s)", h.SocketMode)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Daemon: %s", h.Daemon)
	if h.Daemon == "running" {
		fmt.Fprintf(w, ", %d team(s)", h.TeamsCount)
	}
	fmt.Fprintf(w, "\nUser: %q (%s)\n", h.User, h.UserSource)
	if len(h.Problems) == 0 {
		fmt.Fprintln(w, "OK No problems found")
		return
	}
	fmt.Fprintln(w, "Problems:")
	for _, p := range h.Problems {
		fmt.Fprintf(w, "  - %s\n", p)
	}
}
