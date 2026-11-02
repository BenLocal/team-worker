package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

// gitRepo makes a repository on main with one commit and returns its root.
func gitRepo(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := filepath.Join(t.TempDir(), name)
	gitIn(t, "", "init", "-q", "-b", "main", root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "initial")
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestWorkspaceSpawnMergeAndCleanup(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	repo := gitRepo(t, "app")

	var res model.SpawnResult
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--task", "work", "--repo", repo, "--", "sh", "-c", "sleep 30")
	if len(res.Workspaces) != 1 || res.Workspace != res.Workspaces[0].WorktreePath ||
		res.Workspaces[0].BranchName != "clawteam/demo/alice" || res.Workspaces[0].BaseBranch != "main" {
		t.Fatalf("spawn: %+v", res)
	}
	tree := res.Workspace
	if got := gitIn(t, tree, "symbolic-ref", "--short", "HEAD"); got != "clawteam/demo/alice" {
		t.Fatalf("the agent works on %q", got)
	}
	var list struct{ Workspaces []model.Workspace }
	h.json(&list, "workspace", "list", "demo")
	if len(list.Workspaces) != 1 || list.Workspaces[0].RepoRoot != repo {
		t.Fatalf("list: %+v", list)
	}

	if err := os.WriteFile(filepath.Join(tree, "README.md"), []byte("alice was here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var op model.WorkspaceOp
	h.json(&op, "workspace", "status", "demo", "alice")
	if len(op.Repos) != 1 || !strings.Contains(op.Repos[0].Output, "README.md") {
		t.Fatalf("status: %+v", op)
	}
	h.json(&op, "workspace", "checkpoint", "demo", "alice")
	if op.Status != "checkpoint_created" {
		t.Fatalf("checkpoint: %+v", op)
	}
	h.json(&op, "workspace", "checkpoint", "demo", "alice")
	if op.Status != "no_changes" {
		t.Fatalf("second checkpoint: %+v", op)
	}
	h.json(&op, "workspace", "merge", "demo", "alice")
	if op.Status != "merged" {
		t.Fatalf("merge: %+v", op)
	}
	if got := gitIn(t, repo, "show", "main:README.md"); got != "alice was here" {
		t.Fatalf("main has %q", got)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatal("the merged worktree must be removed")
	}
	if out := gitIn(t, repo, "branch", "--list", "clawteam/demo/alice"); out != "" {
		t.Fatalf("the merged branch must be deleted: %q", out)
	}
	h.json(&list, "workspace", "list", "demo")
	if len(list.Workspaces) != 0 {
		t.Fatalf("list after merge: %+v", list)
	}

	// Deleting the team removes worktrees but keeps branches with work
	// that was never merged.
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "bob", "--task", "work", "--repo", repo, "--", "sh", "-c", "sleep 30")
	if err := os.WriteFile(filepath.Join(res.Workspace, "bob.txt"), []byte("bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cleaned map[string]string
	h.json(&cleaned, "team", "cleanup", "demo", "--force")
	if _, err := os.Stat(res.Workspace); !os.IsNotExist(err) {
		t.Fatal("team cleanup must remove the worktree")
	}
	if got := gitIn(t, repo, "show", "clawteam/demo/bob:bob.txt"); got != "bob" {
		t.Fatalf("bob's uncommitted work must be checkpointed onto the kept branch, got %q", got)
	}
}

func TestWorkspaceSpawnChoices(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	app, web := gitRepo(t, "app"), gitRepo(t, "web")
	plain := t.TempDir()

	// Several repositories: one worktree each under the agent's directory.
	var res model.SpawnResult
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--repo", app, "--repo", web, "--dry-run", "--", "true")
	if len(res.Workspaces) != 2 || res.Workspaces[0].WorktreePath != filepath.Join(res.Workspace, "app") ||
		res.Workspaces[1].WorktreePath != filepath.Join(res.Workspace, "web") {
		t.Fatalf("two repositories: %+v", res)
	}
	if !strings.Contains(res.Prompt, "clawteam/demo/alice") {
		t.Fatal("the prompt must name the agent's branch")
	}
	// --no-workspace works in the repository itself.
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--repo", app, "--no-workspace", "--dry-run", "--", "true")
	if len(res.Workspaces) != 0 || res.Workspace != app {
		t.Fatalf("--no-workspace: %+v", res)
	}
	// An explicit workspace needs a repository; a plain --cwd is left alone.
	if out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "alice", "--repo", plain, "--dry-run", "--", "true"); ok ||
		!strings.Contains(out, "not a git repository") {
		t.Fatalf("a non-repository --repo: ok=%v %s", ok, out)
	}
	t.Setenv("TEAM_WORKER_WORKSPACE", "always")
	if out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "alice", "--cwd", plain, "--workspace", "--dry-run", "--", "true"); ok {
		t.Fatalf("--cwd with --workspace must be refused: %s", out)
	}
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--cwd", plain, "--dry-run", "--", "true")
	if len(res.Workspaces) != 0 || res.Workspace != plain {
		t.Fatalf("--cwd: %+v", res)
	}
}

func TestWorkspaceImportTakesOverClawTeamWorktrees(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	repo := gitRepo(t, "app")
	// ClawTeam made alice's worktree outside team-worker's workspace directory.
	tree := filepath.Join(t.TempDir(), "clawteam", "alice")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "clawteam/demo/alice", tree, "main")
	tree, _ = filepath.EvalSymlinks(tree)
	registry := filepath.Join(t.TempDir(), "workspace-registry.json")
	body := `{"team_name":"demo","repo_root":"` + repo + `","workspaces":[
	  {"agent_name":"alice","agent_id":"a1","team_name":"demo","branch_name":"clawteam/demo/alice","worktree_path":"` + tree + `","repo_root":"` + repo + `","base_branch":"main","created_at":"2026-10-27T08:53:58+00:00"},
	  {"agent_name":"bob","agent_id":"b1","team_name":"demo","branch_name":"clawteam/demo/bob","worktree_path":"/gone/bob","repo_root":"` + repo + `","base_branch":"main","created_at":"2026-10-27T08:53:58+00:00"}]}`
	if err := os.WriteFile(registry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h.run("team", "spawn-team", "demo")
	var op model.WorkspaceOp
	h.json(&op, "workspace", "import", "demo", "--file", registry)
	if op.Count != 1 || len(op.Repos) != 2 || op.Repos[1].Status != "skipped" {
		t.Fatalf("import: %+v", op)
	}
	// Spawning alice again works in the imported worktree where it is.
	var res model.SpawnResult
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--repo", repo, "--dry-run", "--", "true")
	if res.Workspace != tree || len(res.Workspaces) != 1 || res.Workspaces[0].WorktreePath != tree {
		t.Fatalf("spawn after import: %+v", res)
	}
	// A repository named through another agent's worktree is still the
	// repository itself, with its checked-out branch as the base.
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "carol", "--repo", tree, "--dry-run", "--", "true")
	if len(res.Workspaces) != 1 || res.Workspaces[0].RepoRoot != repo || res.Workspaces[0].BaseBranch != "main" {
		t.Fatalf("spawn from a worktree: %+v", res)
	}
}
