package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runHealth runs config health and decodes its JSON.
func runHealth(t *testing.T, h *harness) (health, bool) {
	t.Helper()
	out, ok := h.run("--json", "config", "health")
	var got health
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return got, ok
}

// listenSocket serves a socket at a short path, as unix socket paths are
// limited in length, and points TEAM_WORKER_SOCKET at it.
func listenSocket(t *testing.T) *net.UnixListener {
	t.Helper()
	dir, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAM_WORKER_SOCKET", path)
	return l
}

func TestHealthWithoutDaemon(t *testing.T) {
	h := newHarness(t)
	got, ok := runHealth(t, h)
	if !ok || got.Mode != "local" || !got.Exists || !got.Writable || got.DataDirSource != "env" ||
		got.Daemon != "not running" || len(got.Problems) != 0 {
		t.Fatalf("%v %+v", ok, got)
	}
	if _, err := os.Stat(filepath.Join(got.DataDir, ".health-check")); !os.IsNotExist(err) {
		t.Fatalf("probe file left behind: %v", err)
	}
}

func TestHealthReachesDaemonThroughSocket(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	listenSocket(t)
	got, ok := runHealth(t, h)
	if !ok || got.Daemon != "running" || got.TeamsCount != 1 || got.SocketMode != "0600" {
		t.Fatalf("%v %+v", ok, got)
	}

	if err := os.Chmod(got.Socket, 0o666); err != nil {
		t.Fatal(err)
	}
	got, ok = runHealth(t, h)
	if ok || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "open to other users") {
		t.Fatalf("a socket other users can reach is a problem: %v %+v", ok, got)
	}
}

func TestHealthReportsStaleSocketWithoutStartingDaemon(t *testing.T) {
	h := newHarness(t)
	l := listenSocket(t)
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got, ok := runHealth(t, h)
	if ok || got.Daemon != "unreachable" || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "does not answer") {
		t.Fatalf("%v %+v", ok, got)
	}
}

func TestHealthReportsDatabaseOfAnotherUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens any file")
	}
	h := newHarness(t)
	db := filepath.Join(os.Getenv("TEAM_WORKER_DATA_DIR"), "team-worker.db")
	if err := os.WriteFile(db, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	got, ok := runHealth(t, h)
	if ok || got.DatabaseWritable || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "cannot be opened for writing") {
		t.Fatalf("%v %+v", ok, got)
	}
}

func TestHealthRemoteNeedsAPIKey(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("TEAM_WORKER_API_KEY", "")
	got, ok := runHealth(t, h)
	if ok || got.Mode != "remote" || got.ServerURLSource != "env" || got.APIKeySet || got.Daemon != "running" ||
		len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "TEAM_WORKER_API_KEY") {
		t.Fatalf("%v %+v", ok, got)
	}
	t.Setenv("TEAM_WORKER_API_KEY", "secret")
	if got, ok = runHealth(t, h); !ok || !got.APIKeySet {
		t.Fatalf("%v %+v", ok, got)
	}
	if out, _ := h.run("config", "health"); strings.Contains(out, "secret") {
		t.Fatalf("health printed the API key: %s", out)
	}
}
