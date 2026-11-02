package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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

func TestDshSessionDirMatchesDsh(t *testing.T) {
	// Names copied from real ~/.dsh/sessions directories.
	for cwd, want := range map[string]string{
		"/home/node/.clawteam/workspaces/demo/developer": "--home-node-.clawteam-workspaces-demo-developer--",
		"/home/shiben": "--home-shiben--",
		"/home/shiben/.cache/tmp/claude-1000/-home-x": "--home-shiben-.cache-tmp-claude-1000--home-x--",
		"/home/shiben/workspace/github/":              "--home-shiben-workspace-github--",
	} {
		if got := DshSessionDir(cwd); got != want {
			t.Errorf("DshSessionDir(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestCaptureDshSessionMatchesTheAgentsLog(t *testing.T) {
	root := t.TempDir()
	cwd := "/work/team-1/.agents/alice"
	dir := filepath.Join(root, DshSessionDir(cwd))
	started := time.Now().Add(-time.Minute)
	session := func(id, logCwd string, at time.Time, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, id), 0o700); err != nil {
			t.Fatal(err)
		}
		header := fmt.Sprintf(`{"type":"session","version":4,"id":%q,"createdAt":%d,"cwd":%q}`, id, at.UnixMilli(), logCwd)
		writeZstd(t, filepath.Join(dir, id, "session.v4.jsonl.zstd"), header+"\n"+body)
	}
	identity := `{"type":"message","content":"- ID: agent-alice\n- Name: alice"}` + "\n"
	if id, err := CaptureDshSession(root, cwd, "agent-alice", started); err != nil || id != "" {
		t.Fatalf("no sessions yet: %q, %v", id, err)
	}
	// Earlier runs, other agents, other workspaces and non-session directories do not count.
	session("241a162e-3ad2-435d-8a93-970feb645960", cwd, started.Add(-time.Hour), identity)
	session("8c0f7a51-0d3e-4b55-9d7e-2a8f6c1e4b10", cwd, started.Add(time.Second), strings.ReplaceAll(identity, "agent-alice", "agent-alice2"))
	session("3f6d2c1b-7a8e-4f90-b1c2-d3e4f5a6b7c8", "/work/other", started.Add(time.Second), identity)
	if err := os.MkdirAll(filepath.Join(dir, "not-a-session"), 0o700); err != nil {
		t.Fatal(err)
	}
	if id, err := CaptureDshSession(root, cwd, "agent-alice", started); err != nil || id != "" {
		t.Fatalf("no matching session: %q, %v", id, err)
	}
	session("5ec47ecf-a539-4f6b-adac-e9b9f5334b56", cwd, started.Add(9*time.Second), identity)
	if id, err := CaptureDshSession(root, cwd, "agent-alice", started); err != nil || id != "5ec47ecf-a539-4f6b-adac-e9b9f5334b56" {
		t.Fatalf("want the agent's session, got %q, %v", id, err)
	}
	// A second matching session is ambiguous.
	session("dbac210b-996b-4785-942e-8a37d37d6beb", cwd, started.Add(20*time.Second), identity)
	if id, err := CaptureDshSession(root, cwd, "agent-alice", started); err == nil || id != "" {
		t.Fatalf("two matching sessions must not pick one: %q, %v", id, err)
	}
}

func writeZstd(t *testing.T, path, text string) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	if err := os.WriteFile(path, enc.EncodeAll([]byte(text), nil), 0o600); err != nil {
		t.Fatal(err)
	}
}
