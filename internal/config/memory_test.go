package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitEmptyMemberUserDoesNotInheritConfigUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(FilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FilePath(), []byte(`{"user":"configured-user"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAM_WORKER_USER", "")
	if got := IdentityFromEnv().User; got != "" {
		t.Fatalf("empty identity inherited %q", got)
	}
	if err := os.Unsetenv("TEAM_WORKER_USER"); err != nil {
		t.Fatal(err)
	}
	if got := IdentityFromEnv().User; got != "configured-user" {
		t.Fatal(got)
	}
}
