package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// opencodeDB is a stand-in for opencode.db with the columns capture reads,
// as opencode 1.18 lays them out.
type opencodeDB struct {
	t    *testing.T
	path string
	db   *sql.DB
}

func newOpencodeDB(t *testing.T) *opencodeDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			directory TEXT NOT NULL, time_created INTEGER NOT NULL);
		CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, data TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	return &opencodeDB{t: t, path: path, db: db}
}

// add records a session whose first part is prompt.
func (o *opencodeDB) add(id, parent, dir string, created time.Time, prompt string) {
	o.t.Helper()
	data, _ := json.Marshal(map[string]string{"type": "text", "text": prompt})
	var parentID any
	if parent != "" {
		parentID = parent
	}
	if _, err := o.db.Exec("INSERT INTO session VALUES (?, 'p', ?, ?, ?)", id, parentID, dir, created.UnixMilli()); err != nil {
		o.t.Fatal(err)
	}
	if _, err := o.db.Exec("INSERT INTO part VALUES (?, 'm', ?, ?, ?)", "prt_"+id, id, created.UnixMilli(), string(data)); err != nil {
		o.t.Fatal(err)
	}
}

func agentPrompt(id string) string {
	return "You are a member of team demo.\n- Name: alice\n- ID: " + id + "\n- Team: demo\n"
}

func TestCaptureOpencodeSessionMatchesTheAgentsSession(t *testing.T) {
	ctx := context.Background()
	o := newOpencodeDB(t)
	dir := t.TempDir()
	start := time.Now()
	o.add("ses_old", "", dir, start.Add(-time.Hour), agentPrompt("a1")) // an earlier run
	o.add("ses_other_dir", "", t.TempDir(), start.Add(time.Second), agentPrompt("a1"))
	o.add("ses_other_agent", "", dir, start.Add(time.Second), agentPrompt("a12")) // a1 is a prefix
	o.add("ses_main", "", dir, start.Add(2*time.Second), agentPrompt("a1"))
	o.add("ses_sub", "ses_main", dir, start.Add(3*time.Second), agentPrompt("a1")) // a subagent

	if id, err := CaptureOpencodeSession(ctx, o.path, dir, "a1", start); err != nil || id != "ses_main" {
		t.Fatalf("got %q %v", id, err)
	}
	if id, err := CaptureOpencodeSession(ctx, o.path, dir, "", start); err != nil || id != "" {
		t.Fatalf("no agent id must find nothing: %q %v", id, err)
	}
	if id, err := CaptureOpencodeSession(ctx, filepath.Join(t.TempDir(), "missing.db"), dir, "a1", start); err != nil || id != "" {
		t.Fatalf("missing database: %q %v", id, err)
	}

	o.add("ses_again", "", dir, start.Add(4*time.Second), agentPrompt("a1"))
	if _, err := CaptureOpencodeSession(ctx, o.path, dir, "a1", start); err == nil || !strings.Contains(err.Error(), "multiple opencode sessions") {
		t.Fatalf("two matches must not pick one: %v", err)
	}
}

func TestCaptureOpencodeSessionMatchesWorkspaceAliases(t *testing.T) {
	o := newOpencodeDB(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	o.add("ses_main", "", real, start, agentPrompt("a1"))
	if id, err := CaptureOpencodeSession(context.Background(), o.path, link, "a1", start); err != nil || id != "ses_main" {
		t.Fatalf("got %q %v", id, err)
	}
}
