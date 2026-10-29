package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureCodexSessionMatchesWorkspaceAliases(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, cwd, recordedCwd, want string
	}{
		{"alias to workspace", alias, workspace, "alice-session"},
		{"workspace to alias", workspace, alias, "alice-session"},
		{"different workspace", dir, workspace, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transcripts := t.TempDir()
			meta, err := json.Marshal(map[string]any{
				"type": "session_meta", "payload": map[string]string{"id": "alice-session", "cwd": tt.recordedCwd},
			})
			if err != nil {
				t.Fatal(err)
			}
			data := append(meta, []byte("\n"+`{"type":"message","text":"- ID: alice-id"}`+"\n")...)
			if err := os.WriteFile(filepath.Join(transcripts, "session.jsonl"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := CaptureCodexSession(transcripts, tt.cwd, "alice-id", time.Time{})
			if err != nil || id != tt.want {
				t.Fatalf("got %q, %v; want %q", id, err, tt.want)
			}
		})
	}
}

func TestCaptureCodexSessionDoesNotPickAnotherAgentInSameDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alice", "bob"} {
		data := `{"type":"session_meta","payload":{"id":"` + name + `-session","cwd":"/shared"}}` + "\n" + `{"type":"message","text":"- ID: ` + name + `-id"}`
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := CaptureCodexSession(dir, "/shared", "alice-id", time.Time{})
	if err != nil || id != "alice-session" {
		t.Fatalf("%s %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "duplicate.jsonl"), []byte(`{"type":"session_meta","payload":{"id":"another","cwd":"/shared"}}`+"\n"+`{"text":"- ID: alice-id"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureCodexSession(dir, "/shared", "alice-id", time.Time{}); err == nil {
		t.Fatal("ambiguous sessions must not silently select latest")
	}
}
