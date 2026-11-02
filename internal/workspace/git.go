// Package workspace manages agents' git worktrees on the daemon's machine:
// creating them for spawn, checkpoints, merges and cleanup. Only the daemon
// uses it; clients reach it through the API.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run runs git in dir and returns its trimmed stdout and exit code. A
// failure to run git at all is an error; a non-zero exit is not, so callers
// can read codes such as merge-tree's 1 for conflicts.
func run(ctx context.Context, dir string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Never stop for credentials or an editor: nobody is there to answer.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), exit.ExitCode(), nil
	}
	if err != nil {
		return "", "", -1, fmt.Errorf("run git: %w", err)
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), 0, nil
}

// git runs git in dir and fails unless it exits 0.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, errOut, code, err := run(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := errOut
		if msg == "" {
			msg = out
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// Toplevel returns the root of the git repository dir is in, with symlinks
// resolved, or an error when dir is not in one.
func Toplevel(ctx context.Context, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	out, _, code, err := run(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if code != 0 || out == "" {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return realPath(out), nil
}

func realPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// CurrentBranch is the branch checked out at root, or the short commit id
// when HEAD is detached, as ClawTeam records the base.
func CurrentBranch(ctx context.Context, root string) (string, error) {
	if out, _, code, err := run(ctx, root, "symbolic-ref", "--short", "-q", "HEAD"); err != nil {
		return "", err
	} else if code == 0 && out != "" {
		return out, nil
	}
	return git(ctx, root, "rev-parse", "--short", "HEAD")
}

// BranchName is the branch an agent's worktrees are on, ClawTeam's naming.
func BranchName(team, agent string) string { return "clawteam/" + team + "/" + agent }

// Paths places an agent's worktrees under its workspace directory dir: dir
// itself for one repository, as ClawTeam does, else one subdirectory per
// repository, named after it and numbered when names repeat.
func Paths(dir string, roots []string) []string {
	if len(roots) == 1 {
		return []string{dir}
	}
	seen := map[string]int{}
	out := make([]string, len(roots))
	for i, root := range roots {
		name := filepath.Base(root)
		seen[name]++
		if n := seen[name]; n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		out[i] = filepath.Join(dir, name)
	}
	return out
}

// IsWorktreeOf reports whether path is the top of a worktree of the
// repository at root.
func IsWorktreeOf(ctx context.Context, path, root string) bool {
	top, err := Toplevel(ctx, path)
	if err != nil || top != realPath(path) {
		return false
	}
	mine, err1 := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	theirs, err2 := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return err1 == nil && err2 == nil && realPath(mine) == realPath(theirs)
}

func branchExists(ctx context.Context, root, branch string) (bool, error) {
	_, _, code, err := run(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return code == 0, err
}

// Create adds a worktree of root at path on branch. An existing branch is
// checked out as it is, keeping an earlier run's commits; otherwise the
// branch is created from base. It reports whether it created the branch.
func Create(ctx context.Context, root, path, branch, base string) (bool, error) {
	if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
		return false, fmt.Errorf("%s already exists and is not this agent's worktree; "+
			"remove it, or spawn with --cwd or --no-workspace", path)
	}
	// Forget worktrees whose directories are gone, so the branch is free.
	if _, err := git(ctx, root, "worktree", "prune"); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	exists, err := branchExists(ctx, root, branch)
	if err != nil {
		return false, err
	}
	if exists {
		_, err = git(ctx, root, "worktree", "add", path, branch)
		return false, err
	}
	_, err = git(ctx, root, "worktree", "add", "-b", branch, path, base)
	return err == nil, err
}

// Remove deletes the worktree at path, with any uncommitted changes.
func Remove(ctx context.Context, root, path string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := git(ctx, root, "worktree", "remove", "--force", path); err != nil {
			return err
		}
	}
	_, err := git(ctx, root, "worktree", "prune")
	return err
}

// DeleteBranch deletes branch from root.
func DeleteBranch(ctx context.Context, root, branch string) error {
	if exists, err := branchExists(ctx, root, branch); err != nil || !exists {
		return err
	}
	_, err := git(ctx, root, "branch", "-D", branch)
	return err
}

// DeleteBranchIfMerged deletes branch when base already contains it, and
// keeps it otherwise so unmerged commits are not lost. It reports whether
// the branch is gone.
func DeleteBranchIfMerged(ctx context.Context, root, branch, base string) (bool, error) {
	exists, err := branchExists(ctx, root, branch)
	if err != nil || !exists {
		return err == nil, err
	}
	_, _, code, err := run(ctx, root, "merge-base", "--is-ancestor", "refs/heads/"+branch, base)
	if err != nil || code != 0 {
		return false, err
	}
	return true, DeleteBranch(ctx, root, branch)
}

// Checkpoint commits everything in the worktree at path, untracked files
// included. It reports false when there was nothing to commit.
func Checkpoint(ctx context.Context, path, message string) (bool, error) {
	if _, err := git(ctx, path, "add", "-A"); err != nil {
		return false, err
	}
	_, _, code, err := run(ctx, path, "diff", "--cached", "--quiet")
	if err != nil || code == 0 {
		return false, err
	}
	if _, err := git(ctx, path, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// DiffStat summarizes the worktree's staged and unstaged changes.
func DiffStat(ctx context.Context, path string) (string, error) {
	staged, err := git(ctx, path, "diff", "--cached", "--stat")
	if err != nil {
		return "", err
	}
	unstaged, err := git(ctx, path, "diff", "--stat")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.Join([]string{staged, unstaged}, "\n")), nil
}
