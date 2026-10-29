package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerURLConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TEAM_WORKER_SERVER_URL", "")
	if err := os.MkdirAll(filepath.Dir(FilePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FilePath(), []byte(`{"server_url":"http://configured.example:8082"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("TEAM_WORKER_SERVER_URL"); err != nil {
		t.Fatal(err)
	}
	if got := ServerURL(); got != "http://configured.example:8082" {
		t.Fatal(got)
	}
	t.Setenv("TEAM_WORKER_SERVER_URL", "http://override.example:8082")
	if got := ServerURL(); got != "http://override.example:8082" {
		t.Fatal(got)
	}
	t.Setenv("TEAM_WORKER_SERVER_URL", "")
	if got := ServerURL(); got != "" {
		t.Fatalf("empty environment value must select the socket: %s", got)
	}
}
