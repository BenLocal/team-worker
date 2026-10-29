package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
[template]
name = "pair"
description = "two agents"
[template.env]
SHARED = "1"

[template.leader]
name = "lead"
task = "lead {goal}"

[[template.agents]]
name = "dev"
type = "developer"
command = ["codex"]
env = { ROLE = "dev" }
task = "build {goal} for {leader_name}"

[[template.tasks]]
key = "build"
subject = "Build"
owner = "dev"

[[template.tasks]]
subject = "Review"
owner = "lead"
blocked_by = ["build"]
`

func TestListAndLoadUserTemplates(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pair.toml"), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "pair" || len(list[0].Agents) != 1 || len(list[0].Tasks) != 2 {
		t.Fatalf("list: %+v", list)
	}
	tmpl, err := Load("pair", dir)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Env["SHARED"] != "1" || tmpl.Agents[0].Env["ROLE"] != "dev" {
		t.Fatalf("env: %+v %+v", tmpl.Env, tmpl.Agents[0].Env)
	}
	if tmpl.Command[0] != "claude" || tmpl.Backend != "tmux" || tmpl.Agents[0].Command[0] != "codex" {
		t.Fatalf("defaults: %+v", tmpl)
	}
	byPath, err := Load(filepath.Join(dir, "pair.toml"), "/nonexistent")
	if err != nil || byPath.Name != "pair" {
		t.Fatalf("load by path: %+v %v", byPath, err)
	}
}

func TestEmptyDirHasNoTemplates(t *testing.T) {
	list, err := List(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(list) != 0 {
		t.Fatalf("want none, got %+v %v", list, err)
	}
	_, err = Load("x", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "docs/templates.md") {
		t.Fatalf("missing template error should point to the docs: %v", err)
	}
}

func TestUnknownBlockedByKeyIsRejected(t *testing.T) {
	_, err := parse([]byte(`
[template]
[template.leader]
name = "lead"
[[template.tasks]]
subject = "b"
blocked_by = ["missing"]
`), "x", "test")
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestRender(t *testing.T) {
	got := Render("{goal} in {team_name} by {agent_name} for {leader_name} {other}", "ship", "t1", "bob", "lead")
	if got != "ship in t1 by bob for lead {other}" {
		t.Fatalf("render: %q", got)
	}
}
