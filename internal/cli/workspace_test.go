package cli

import (
	"context"
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

func TestTeamDefaultRepositories(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	app, web := gitRepo(t, "app"), gitRepo(t, "web")
	h.run("team", "spawn-team", "demo", "--repo", app)
	var repos struct{ Repos []string }
	h.json(&repos, "team", "repos", "demo")
	if len(repos.Repos) != 1 || repos.Repos[0] != app {
		t.Fatalf("repos: %+v", repos)
	}
	// spawn without --repo uses the team's repositories; --repo wins.
	var res model.SpawnResult
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--dry-run", "--", "true")
	if len(res.Workspaces) != 1 || res.Workspaces[0].RepoRoot != app {
		t.Fatalf("team default: %+v", res)
	}
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--repo", web, "--dry-run", "--", "true")
	if len(res.Workspaces) != 1 || res.Workspaces[0].RepoRoot != web {
		t.Fatalf("--repo over the default: %+v", res)
	}
	res = model.SpawnResult{}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--no-workspace", "--dry-run", "--", "true")
	if len(res.Workspaces) != 0 {
		t.Fatalf("--no-workspace ignores the default: %+v", res)
	}
	h.json(&repos, "team", "repos", "demo", app, web)
	if len(repos.Repos) != 2 {
		t.Fatalf("set: %+v", repos)
	}
	if out, ok := h.run("--json", "team", "repos", "demo", t.TempDir()); ok || !strings.Contains(out, "not a git repository") {
		t.Fatalf("a non-repository must be refused: ok=%v %s", ok, out)
	}
	repos.Repos = nil
	h.json(&repos, "team", "repos", "demo", "--clear")
	if len(repos.Repos) != 0 {
		t.Fatalf("clear: %+v", repos)
	}
}

func TestLaunchGivesTemplateAgentsWorktrees(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	app, web := gitRepo(t, "app"), gitRepo(t, "web")
	tmpl := filepath.Join(t.TempDir(), "squad.toml")
	body := `[template]
name = "squad"
backend = "subprocess"
command = ["sh", "-c", "sleep 30"]
repos = ["` + app + `"]
[template.leader]
name = "lead"
[[template.agents]]
name = "front"
repos = ["` + web + `"]
`
	if err := os.WriteFile(tmpl, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out model.LaunchResult
	h.json(&out, "launch", tmpl, "--team", "squad")
	if out.Status != "launched" {
		t.Fatalf("launch: %+v", out)
	}
	var list struct{ Workspaces []model.Workspace }
	h.json(&list, "workspace", "list", "squad")
	got := map[string]string{}
	for _, ws := range list.Workspaces {
		got[ws.AgentName] = ws.RepoRoot
	}
	if got["lead"] != app || got["front"] != web {
		t.Fatalf("worktrees: %+v", list.Workspaces)
	}
	var team model.Team
	h.json(&team, "team", "status", "squad")
	if len(team.Repos) != 1 || team.Repos[0] != app {
		t.Fatalf("the template's repos become the team's: %+v", team.Repos)
	}
	h.run("team", "cleanup", "squad", "--force")
}

func TestContextAcrossAgentWorktrees(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	repo := gitRepo(t, "app")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "shared")
	trees := map[string]string{}
	for _, name := range []string{"alice", "bob"} {
		var res model.SpawnResult
		h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", name, "--task", "work", "--repo", repo, "--", "sh", "-c", "sleep 30")
		trees[name] = res.Workspace
	}
	t.Cleanup(func() { h.run("team", "cleanup", "demo", "--force") })
	edit := func(name, file, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(trees[name], file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Both change README's only line (high); shared.txt on different lines (medium).
	edit("alice", "README.md", "alice\n")
	edit("alice", "shared.txt", "1\ntwo\n3\n4\n5\n6\n7\n8\n9\n10\n")
	edit("bob", "README.md", "bob\n")
	edit("bob", "shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\nnine\n10\n")
	edit("bob", "bob.txt", "bob\n")
	var op model.WorkspaceOp
	h.json(&op, "workspace", "checkpoint", "demo", "alice", "-m", "alice | readme")
	h.json(&op, "workspace", "checkpoint", "demo", "bob", "-m", "bob work")

	var diff model.AgentDiff
	h.json(&diff, "context", "diff", "demo", "alice")
	if len(diff.FilesChanged) != 2 || diff.CommitCount != 1 || !strings.HasPrefix(diff.Summary, "alice: 2 file(s)") {
		t.Fatalf("diff: %+v", diff)
	}
	var files map[string][]string
	h.json(&files, "context", "files", "demo")
	if len(files["README.md"]) != 2 || len(files["bob.txt"]) != 1 {
		t.Fatalf("files: %v", files)
	}
	var conflicts []model.FileOverlap
	h.json(&conflicts, "context", "conflicts", "demo")
	if len(conflicts) != 2 || conflicts[0].File != "README.md" || conflicts[0].Severity != "high" ||
		conflicts[1].File != "shared.txt" || conflicts[1].Severity != "medium" {
		t.Fatalf("conflicts: %+v", conflicts)
	}
	var log []model.AgentCommit
	h.json(&log, "context", "log", "demo")
	if len(log) != 2 {
		t.Fatalf("log: %+v", log)
	}
	messages := log[0].Message + " / " + log[1].Message
	if !strings.Contains(messages, "alice | readme") {
		t.Fatalf("a subject with | must survive: %s", messages)
	}

	// alice's task waits for bob's, so bob's work is upstream for her.
	var bobTask, aliceTask model.Task
	h.json(&bobTask, "task", "create", "demo", "api", "-o", "bob")
	h.json(&aliceTask, "task", "create", "demo", "ui", "-o", "alice", "--blocked-by", bobTask.ID)
	var injected map[string]string
	h.json(&injected, "context", "inject", "demo", "alice", "--send")
	text := injected["context"]
	for _, want := range []string{"# Git Context for alice", "## File Overlap Warnings", "`README.md` also modified by: bob",
		"## Recent Related Changes", "[bob]", "## Upstream Dependency Changes", "- bob: bob: 3 file(s)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("inject lacks %q:\n%s", want, text)
		}
	}
	msgs, err := h.api.Peek(context.Background(), "demo", "alice", "")
	if err != nil || len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "# Git Context for alice") {
		t.Fatalf("--send must deliver the block: %+v %v", msgs, err)
	}
}
