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

func TestConfigSetGetShow(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(os.Getenv("HOME"), ".team-worker", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"remotes":{"prod":{"server_url":"http://x","api_key":"k"}},"profiles":{"p":{"agent":"claude"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got map[string]string
	h.json(&got, "config", "get", "user")
	if got["value"] != "" || got["source"] != "default" {
		t.Fatalf("unset user: %v", got)
	}
	if out, ok := h.run("config", "set", "user", "ann"); !ok || !strings.Contains(out, "user = ann") {
		t.Fatalf("set: %v %s", ok, out)
	}
	h.json(&got, "config", "get", "user")
	if got["value"] != "ann" || got["source"] != "file" {
		t.Fatalf("saved user: %v", got)
	}

	var show map[string]configValue
	h.json(&show, "config", "show")
	if show["user"] != (configValue{"ann", "file"}) || show["workspace"] != (configValue{"never", "env"}) ||
		show["data_dir"].Source != "env" || show["server_url"] != (configValue{"", "default"}) {
		t.Fatalf("show: %+v", show)
	}

	// Other keys of the file survive, and an empty value removes the key.
	if _, ok := h.run("config", "set", "user", ""); !ok {
		t.Fatal("clearing user failed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["user"]; ok || raw["remotes"] == nil || raw["profiles"] == nil {
		t.Fatalf("file after clearing user: %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config.json mode: %v %v", info, err)
	}
}

func TestConfigRejectsUnknownKeyAndBadValue(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"config", "get", "nope"},
		{"config", "set", "nope", "x"},
		{"config", "set", "workspace", "sometimes"},
	} {
		if out, ok := h.run(args...); ok {
			t.Fatalf("%v succeeded: %s", args, out)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".team-worker", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("a rejected set must not write the file: %v", err)
	}
}
