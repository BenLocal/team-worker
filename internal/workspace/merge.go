package workspace

import (
	"context"
	"fmt"
	"strings"
)

// MergeItem merges Branch into Target in the repository at Root; Label
// names the repository in the output.
type MergeItem struct {
	Root, Branch, Target, Label string
}

// merge is a checked MergeItem: where Target is checked out, if anywhere,
// and the merged tree when it can be written without a checkout.
type merge struct {
	MergeItem
	checkout   string // worktree with Target checked out, or ""
	targetHead string
	branchHead string
	tree       string
	upToDate   bool
}

// Merge merges every item with --no-ff semantics, or none of them: each is
// first tried with `git merge-tree`, which touches no worktree, and a
// conflict anywhere stops the merge before any branch moves. No branch is
// ever switched: a target checked out in a clean worktree is merged there,
// one checked out nowhere has its ref moved to the new merge commit. It
// returns the output and false when it refused because of conflicts or
// uncommitted changes; err is for failures to run git.
func Merge(ctx context.Context, items []MergeItem) (string, bool, error) {
	var checked []merge
	var problems []string
	for _, it := range items {
		m, problem, err := check(ctx, it)
		if err != nil {
			return "", false, err
		}
		if problem != "" {
			problems = append(problems, it.Label+": "+problem)
			continue
		}
		checked = append(checked, m)
	}
	if len(problems) > 0 {
		return "Nothing was merged.\n" + strings.Join(problems, "\n"), false, nil
	}
	var out []string
	for _, m := range checked {
		line, err := apply(ctx, m)
		if err != nil {
			return strings.Join(append(out, m.Label+": "+err.Error()), "\n"), false, nil
		}
		out = append(out, m.Label+": "+line)
	}
	return strings.Join(out, "\n"), true, nil
}

func check(ctx context.Context, it MergeItem) (merge, string, error) {
	m := merge{MergeItem: it}
	var err error
	if m.targetHead, err = revision(ctx, it.Root, it.Target); err != nil {
		return m, fmt.Sprintf("target branch %s not found", it.Target), nil
	}
	if m.branchHead, err = revision(ctx, it.Root, it.Branch); err != nil {
		return m, fmt.Sprintf("branch %s not found", it.Branch), nil
	}
	if m.checkout, err = checkedOutAt(ctx, it.Root, it.Target); err != nil {
		return m, "", err
	}
	if m.checkout != "" {
		dirty, err := git(ctx, m.checkout, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return m, "", err
		}
		if dirty != "" {
			return m, fmt.Sprintf("%s has uncommitted changes on %s; commit or stash them first", m.checkout, it.Target), nil
		}
	}
	if _, _, code, err := run(ctx, it.Root, "merge-base", "--is-ancestor", m.branchHead, m.targetHead); err != nil {
		return m, "", err
	} else if code == 0 {
		m.upToDate = true
		return m, "", nil
	}
	out, errOut, code, err := run(ctx, it.Root, "merge-tree", "--write-tree", "--name-only", "--no-messages", m.targetHead, m.branchHead)
	if err != nil {
		return m, "", err
	}
	lines := strings.Split(out, "\n")
	switch code {
	case 0:
		m.tree = lines[0]
		return m, "", nil
	case 1:
		files := strings.Join(nonEmpty(lines[1:]), ", ")
		return m, fmt.Sprintf("%s conflicts with %s in %s", it.Branch, it.Target, files), nil
	}
	return m, "", fmt.Errorf("git merge-tree (git 2.38 or later is required): %s", errOut)
}

func apply(ctx context.Context, m merge) (string, error) {
	if m.upToDate {
		return fmt.Sprintf("%s already contains %s", m.Target, m.Branch), nil
	}
	msg := fmt.Sprintf("Merge branch '%s' into %s", m.Branch, m.Target)
	if m.checkout != "" {
		if _, err := git(ctx, m.checkout, "merge", "--no-ff", "-m", msg, m.Branch); err != nil {
			run(ctx, m.checkout, "merge", "--abort")
			return "", err
		}
		head, err := git(ctx, m.checkout, "rev-parse", "--short", "HEAD")
		return fmt.Sprintf("merged %s into %s (%s)", m.Branch, m.Target, head), err
	}
	commit, err := git(ctx, m.Root, "commit-tree", m.tree, "-p", m.targetHead, "-p", m.branchHead, "-m", msg)
	if err != nil {
		return "", err
	}
	// Moving the ref only if it is still where it was checked keeps a
	// concurrent commit to the target from being lost.
	if _, err := git(ctx, m.Root, "update-ref", "refs/heads/"+m.Target, commit, m.targetHead); err != nil {
		return "", err
	}
	return fmt.Sprintf("merged %s into %s (%s)", m.Branch, m.Target, commit[:min(len(commit), 7)]), nil
}

func revision(ctx context.Context, root, branch string) (string, error) {
	return git(ctx, root, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
}

// checkedOutAt returns the worktree of root's repository that has branch
// checked out, or "".
func checkedOutAt(ctx context.Context, root, branch string) (string, error) {
	out, err := git(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	var path string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = p
		}
		if line == "branch refs/heads/"+branch {
			return path, nil
		}
	}
	return "", nil
}

func nonEmpty(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
