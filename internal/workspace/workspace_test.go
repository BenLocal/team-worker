package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo makes a repository on main with one commit and returns its root.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := filepath.Join(t.TempDir(), name)
	mustGit(t, "", "init", "-q", "-b", "main", root)
	write(t, filepath.Join(root, "README.md"), "hello\n")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "initial")
	return realPath(root)
}

func mustGit(t *testing.T, dir string, args ...string) string {
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

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReusesTheBranchAndRefusesForeignDirectories(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	path := filepath.Join(t.TempDir(), "alice")
	branch := BranchName("demo", "alice")
	created, err := Create(ctx, root, path, branch, "main")
	if err != nil || !created || !IsWorktreeOf(ctx, path, root) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if got := mustGit(t, path, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("worktree is on %q", got)
	}
	write(t, filepath.Join(path, "work.txt"), "done\n")
	if ok, err := Checkpoint(ctx, path, "checkpoint"); err != nil || !ok {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	if ok, err := Checkpoint(ctx, path, "checkpoint"); err != nil || ok {
		t.Fatalf("a clean worktree has nothing to commit: %v %v", ok, err)
	}
	// Removing the worktree keeps the branch; a new worktree picks it up
	// with the earlier commits.
	if err := Remove(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	if created, err := Create(ctx, root, path, branch, "main"); err != nil || created {
		t.Fatalf("the branch must be reused: created=%v err=%v", created, err)
	}
	if _, err := os.Stat(filepath.Join(path, "work.txt")); err != nil {
		t.Fatal("the earlier commit is missing from the reused branch")
	}
	// A directory with other content is never taken over.
	foreign := filepath.Join(t.TempDir(), "bob")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(foreign, "notes.txt"), "mine\n")
	if _, err := Create(ctx, root, foreign, BranchName("demo", "bob"), "main"); err == nil {
		t.Fatal("a non-empty directory must not become a worktree")
	}
	if IsWorktreeOf(ctx, foreign, root) {
		t.Fatal("a plain directory is not a worktree")
	}
}

func TestPathsNameRepositoriesUnderTheWorkspace(t *testing.T) {
	if got := Paths("/w/alice", []string{"/src/app"}); got[0] != "/w/alice" {
		t.Fatalf("one repository is the workspace itself: %v", got)
	}
	got := Paths("/w/alice", []string{"/src/app", "/other/app", "/src/web"})
	want := []string{"/w/alice/app", "/w/alice/app-2", "/w/alice/web"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDeleteBranchIfMergedKeepsUnmergedWork(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	path := filepath.Join(t.TempDir(), "alice")
	branch := BranchName("demo", "alice")
	if _, err := Create(ctx, root, path, branch, "main"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "work.txt"), "done\n")
	if _, err := Checkpoint(ctx, path, "work"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	if gone, err := DeleteBranchIfMerged(ctx, root, branch, "main"); err != nil || gone {
		t.Fatalf("an unmerged branch must be kept: %v %v", gone, err)
	}
	mustGit(t, root, "merge", "-q", "--no-ff", "-m", "merge", branch)
	if gone, err := DeleteBranchIfMerged(ctx, root, branch, "main"); err != nil || !gone {
		t.Fatalf("a merged branch can go: %v %v", gone, err)
	}
}

// agentWork gives alice a worktree of root with one committed file.
func agentWork(t *testing.T, root, file, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	if _, err := Create(context.Background(), root, path, BranchName("demo", "alice"), "main"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, file), text)
	if _, err := Checkpoint(context.Background(), path, "work"); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMergeIsAllOrNothingAcrossRepositories(t *testing.T) {
	ctx := context.Background()
	app, web := newRepo(t, "app"), newRepo(t, "web")
	branch := BranchName("demo", "alice")
	agentWork(t, app, "feature.txt", "feature\n")
	agentWork(t, web, "README.md", "alice's readme\n")
	// main moves on in web with a conflicting change.
	write(t, filepath.Join(web, "README.md"), "main's readme\n")
	mustGit(t, web, "commit", "-q", "-am", "main change")
	appHead := mustGit(t, app, "rev-parse", "main")
	out, ok, err := Merge(ctx, []MergeItem{{app, branch, "main", "app"}, {web, branch, "main", "web"}})
	if err != nil || ok || !strings.Contains(out, "web: ") || !strings.Contains(out, "README.md") {
		t.Fatalf("the conflict must stop the merge: ok=%v err=%v\n%s", ok, err, out)
	}
	if mustGit(t, app, "rev-parse", "main") != appHead {
		t.Fatal("app was merged although web conflicted")
	}
	// Without the conflict both merge, each with a merge commit.
	out, ok, err = Merge(ctx, []MergeItem{{app, branch, "main", "app"}})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v\n%s", ok, err, out)
	}
	if parents := strings.Fields(mustGit(t, app, "rev-list", "--parents", "-n", "1", "main")); len(parents) != 3 {
		t.Fatalf("want a merge commit, got %v", parents)
	}
	if _, err := os.Stat(filepath.Join(app, "feature.txt")); err != nil {
		t.Fatal("the checked-out main must be updated in place")
	}
	if out, ok, _ := Merge(ctx, []MergeItem{{app, branch, "main", "app"}}); !ok || !strings.Contains(out, "already contains") {
		t.Fatalf("merging again changes nothing: %v %s", ok, out)
	}
}

func TestMergeNeverSwitchesBranches(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	branch := BranchName("demo", "alice")
	agentWork(t, root, "feature.txt", "feature\n")
	// The main checkout is on another branch: main is merged by moving its
	// ref, and the checkout stays where it is.
	mustGit(t, root, "checkout", "-q", "-b", "other")
	out, ok, err := Merge(ctx, []MergeItem{{root, branch, "main", "app"}})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v\n%s", ok, err, out)
	}
	if got := mustGit(t, root, "symbolic-ref", "--short", "HEAD"); got != "other" {
		t.Fatalf("the checkout moved to %q", got)
	}
	if got := mustGit(t, root, "show", "main:feature.txt"); got != "feature" {
		t.Fatalf("main does not have the work: %q", got)
	}
	// Uncommitted changes where the target is checked out stop the merge.
	mustGit(t, root, "checkout", "-q", "main")
	write(t, filepath.Join(root, "README.md"), "edited\n")
	agentPath := filepath.Join(t.TempDir(), "bob")
	if _, err := Create(ctx, root, agentPath, BranchName("demo", "bob"), "main"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(agentPath, "bob.txt"), "bob\n")
	if _, err := Checkpoint(ctx, agentPath, "bob"); err != nil {
		t.Fatal(err)
	}
	if out, ok, err := Merge(ctx, []MergeItem{{root, BranchName("demo", "bob"), "main", "app"}}); err != nil || ok ||
		!strings.Contains(out, "uncommitted changes") {
		t.Fatalf("a dirty checkout must stop the merge: ok=%v err=%v\n%s", ok, err, out)
	}
}

func TestMainRootLeavesLinkedWorktrees(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	leader := filepath.Join(t.TempDir(), "leader")
	if _, err := Create(ctx, root, leader, BranchName("demo", "leader"), "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(leader, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, leader, filepath.Join(leader, "sub")} {
		if got, err := MainRoot(ctx, dir); err != nil || got != root {
			t.Fatalf("MainRoot(%s) = %q, %v; want %s", dir, got, err, root)
		}
	}
	if _, err := MainRoot(ctx, t.TempDir()); err == nil {
		t.Fatal("a plain directory has no repository")
	}
}

func TestContextReadsTheAgentsCommittedWork(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	write(t, filepath.Join(root, "shared.txt"), "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "shared")
	path := filepath.Join(t.TempDir(), "alice")
	if _, err := Create(ctx, root, path, BranchName("demo", "alice"), "main"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "shared.txt"), "1\ntwo\n3\n4\n5\n6\n7\n8\nnine\n10\n")
	write(t, filepath.Join(path, "new.txt"), "a\nb\n")
	if _, err := Checkpoint(ctx, path, "alice | first pass"); err != nil {
		t.Fatal(err)
	}
	head := Head(ctx, root, path, BranchName("demo", "alice"))
	d, err := BranchDiff(ctx, root, "main", head)
	if err != nil || len(d.Files) != 2 || d.Insertions != 4 || d.Deletions != 2 || d.CommitCount != 1 || d.Stat == "" {
		t.Fatalf("diff: %+v %v", d, err)
	}
	lines, err := ChangedLines(ctx, root, "main", head, "shared.txt")
	if err != nil || len(lines) != 2 || !lines[2] || !lines[9] {
		t.Fatalf("changed lines: %v %v", lines, err)
	}
	log, err := Log(ctx, root, "main", head)
	if err != nil || len(log) != 1 || log[0].Message != "alice | first pass" || len(log[0].Files) != 2 || log[0].Timestamp == "" {
		t.Fatalf("log: %+v %v", log, err)
	}
	// Work on another branch inside the worktree still counts.
	mustGit(t, path, "checkout", "-q", "-b", "task-1")
	write(t, filepath.Join(path, "task.txt"), "t\n")
	if _, err := Checkpoint(ctx, path, "task"); err != nil {
		t.Fatal(err)
	}
	if d, _ := BranchDiff(ctx, root, "main", Head(ctx, root, path, BranchName("demo", "alice"))); d.CommitCount != 2 {
		t.Fatalf("the worktree's own branch must be read: %+v", d)
	}
	if d, err := BranchDiff(ctx, root, "main", ""); err != nil || len(d.Files) != 0 {
		t.Fatalf("no work is an empty diff: %+v %v", d, err)
	}
}

func TestCurrentBaseFollowsAnUpstreamThatIsAhead(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "app")
	if got := CurrentBase(ctx, root, "main"); got != "main" {
		t.Fatalf("without an upstream: %q", got)
	}
	// origin/main moves on, as after a fetch; the local main stays behind.
	mustGit(t, root, "remote", "add", "origin", "/nonexistent")
	mustGit(t, root, "config", "branch.main.remote", "origin")
	mustGit(t, root, "config", "branch.main.merge", "refs/heads/main")
	mustGit(t, root, "checkout", "-q", "-b", "upstream-work")
	write(t, filepath.Join(root, "merged.txt"), "merged upstream\n")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "merged upstream")
	mustGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	mustGit(t, root, "checkout", "-q", "main")
	base := CurrentBase(ctx, root, "main")
	if base != "refs/remotes/origin/main" {
		t.Fatalf("a lagging main must give way to origin/main: %q", base)
	}
	// An agent reset to origin/main has done no work of its own.
	head := mustGit(t, root, "rev-parse", "refs/remotes/origin/main")
	if d, err := BranchDiff(ctx, root, base, head); err != nil || len(d.Files) != 0 {
		t.Fatalf("upstream commits are not the agent's work: %+v %v", d, err)
	}
	// A local main with commits origin lacks stays the base.
	write(t, filepath.Join(root, "local.txt"), "local\n")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "local")
	if got := CurrentBase(ctx, root, "main"); got != "main" {
		t.Fatalf("a diverged main stays the base: %q", got)
	}
}
