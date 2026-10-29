package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
