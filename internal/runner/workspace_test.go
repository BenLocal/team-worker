package runner_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/runner"
	"github.com/benlocal/team-worker/internal/server"
	"github.com/benlocal/team-worker/internal/store"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := filepath.Join(t.TempDir(), "app")
	git(t, "", "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "initial")
	resolved, _ := filepath.EvalSymlinks(root)
	return resolved
}

func git(t *testing.T, dir string, args ...string) string {
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

func TestWorkspacesAreReusedAndUndone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	c := server.LocalClient(server.New(st, nil))
	t.Setenv("TEAM_WORKER_WORKSPACE", "auto")
	repo, dataDir := gitRepo(t), t.TempDir()
	req := runner.Request{Team: "demo", Name: "alice", AgentID: "a1", Repos: []string{repo}}

	cwd, undo, err := runner.PrepareWorkspaces(ctx, c, dataDir, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "draft.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second spawn of the same agent keeps its worktree and what is in it;
	// undoing that spawn leaves the worktree alone.
	again, undoAgain, err := runner.PrepareWorkspaces(ctx, c, dataDir, req)
	if err != nil || again != cwd {
		t.Fatalf("reuse: %q %v", again, err)
	}
	undoAgain()
	if _, err := os.Stat(filepath.Join(cwd, "draft.txt")); err != nil {
		t.Fatal("a reused worktree must survive a failed spawn")
	}
	// Undoing the spawn that created it removes the worktree, its new
	// branch and its record.
	undo()
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatal("the worktree must go")
	}
	if out := git(t, repo, "branch", "--list", "clawteam/demo/alice"); out != "" {
		t.Fatalf("the new branch must go: %q", out)
	}
	if list, err := c.ListWorkspaces(ctx, "demo", "alice"); err != nil || len(list) != 0 {
		t.Fatalf("the record must go: %+v %v", list, err)
	}
}
