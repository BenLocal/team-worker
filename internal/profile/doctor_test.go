package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorKeepsOtherClaudeState(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"projects":{"/w":{"hasTrustDialogAccepted":true}},"numStartups":12345678901234567890}`), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := Doctor(home, "Claude")
	if err != nil || res.Created || res.Client != "claude" || res.Path != path || !res.HasCompletedOnboarding {
		t.Fatalf("%+v %v", res, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if string(state["hasCompletedOnboarding"]) != "true" || string(state["numStartups"]) != "12345678901234567890" ||
		state["projects"] == nil {
		t.Fatalf("state: %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %v %v", info, err)
	}
}

func TestDoctorCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	res, err := Doctor(home, "claude-code")
	if err != nil || !res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	info, err := os.Stat(filepath.Join(home, ".claude.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
}

func TestDoctorRefusesUnparsableFileAndOtherClients(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Doctor(home, "claude"); err == nil {
		t.Fatal("an unparsable file must not be replaced")
	}
	if data, _ := os.ReadFile(path); string(data) != "{broken" {
		t.Fatalf("file changed: %s", data)
	}
	if _, err := Doctor(home, "codex"); err == nil {
		t.Fatal("codex is not supported")
	}
}

func TestDoctorWritesThroughSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(t.TempDir(), "claude.json")
	if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Doctor(home, "claude"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", fi, err)
	}
	if data, _ := os.ReadFile(real); !json.Valid(data) || len(data) < 10 {
		t.Fatalf("target not updated: %s", data)
	}
}
