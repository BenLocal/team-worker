package workspace

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// Diff is what an agent's work changes relative to its base branch, from
// where the two diverged; ClawTeam's agent_diff.
type Diff struct {
	Files       []string
	Insertions  int
	Deletions   int
	Stat        string
	CommitCount int
}

// Commit is one commit of an agent's work.
type Commit struct {
	Hash, Message, Timestamp string
	Files                    []string
}

// Head is the commit an agent's work is at: what its worktree has checked
// out, which may be another branch than the one it was given, or the
// branch itself once the worktree is gone. It returns "" when neither
// exists.
func Head(ctx context.Context, root, worktree, branch string) string {
	if _, err := os.Stat(worktree); err == nil {
		if head, err := git(ctx, worktree, "rev-parse", "--verify", "HEAD"); err == nil {
			return head
		}
	}
	head, err := revision(ctx, root, branch)
	if err != nil {
		return ""
	}
	return head
}

// OwnStart is where an agent's own work on head begins: the newest commit
// in base..head that the agent did not make in its worktree, or base when
// it made them all. Reviewers and testers reset their worktrees to another
// agent's branch; those commits are the other agent's work, and counting
// them for whoever checked them out reports every review as a conflict.
// The worktree's HEAD reflog tells them apart, since every agent commits
// under the same git identity. Without a reflog (the worktree is gone)
// everything since base counts.
func OwnStart(ctx context.Context, root, worktree, base, head string) string {
	if head == "" {
		return base
	}
	reflog, _, code, err := run(ctx, worktree, "reflog", "show", "--format=%H %gs", "HEAD")
	if err != nil || code != 0 {
		return base
	}
	made := map[string]bool{}
	for _, line := range strings.Split(reflog, "\n") {
		hash, subject, _ := strings.Cut(line, " ")
		// Entries that create commits here; reset and checkout only move HEAD.
		for _, action := range []string{"commit", "merge", "rebase", "cherry-pick", "revert", "pull"} {
			if strings.HasPrefix(subject, action) {
				made[hash] = true
			}
		}
	}
	commits, _, code, err := run(ctx, root, "rev-list", base+".."+head)
	if err != nil || code != 0 {
		return base
	}
	for _, c := range strings.Split(commits, "\n") {
		if c != "" && !made[c] {
			return c
		}
	}
	return base
}

// Uncommitted counts the changed and untracked files in a worktree.
func Uncommitted(ctx context.Context, worktree string) int {
	if _, err := os.Stat(worktree); err != nil {
		return 0
	}
	out, _, code, err := run(ctx, worktree, "status", "--porcelain")
	if err != nil || code != 0 || out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// CurrentBase is the revision to compare an agent's work with: base, or
// its upstream when base only lags behind it. Teams that fetch in their
// worktrees and never pull in the main checkout leave the local base
// branch behind origin; comparing with it would count everything merged
// upstream since as every agent's work.
func CurrentBase(ctx context.Context, root, base string) string {
	upstream, _, code, err := run(ctx, root, "for-each-ref", "--format=%(upstream)", "refs/heads/"+base)
	if err != nil || code != 0 || upstream == "" {
		return base
	}
	if _, _, code, err := run(ctx, root, "merge-base", "--is-ancestor", "refs/heads/"+base, upstream); err != nil || code != 0 {
		return base
	}
	return upstream
}

// BranchDiff compares head with base from their merge base. A missing
// revision gives an empty diff, as in ClawTeam.
func BranchDiff(ctx context.Context, root, base, head string) (Diff, error) {
	var d Diff
	if head == "" {
		return d, nil
	}
	numstat, _, code, err := run(ctx, root, "diff", "--numstat", "--no-renames", base+"..."+head)
	if err != nil || code != 0 {
		return d, err
	}
	for _, line := range strings.Split(numstat, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		d.Files = append(d.Files, parts[2])
		// Binary files show "-" for both counts.
		if n, err := strconv.Atoi(parts[0]); err == nil {
			d.Insertions += n
		}
		if n, err := strconv.Atoi(parts[1]); err == nil {
			d.Deletions += n
		}
	}
	if d.Stat, _, _, err = run(ctx, root, "diff", "--stat", "--no-renames", base+"..."+head); err != nil {
		return d, err
	}
	count, _, _, err := run(ctx, root, "rev-list", "--count", base+".."+head)
	if err != nil {
		return d, err
	}
	d.CommitCount, _ = strconv.Atoi(count)
	return d, nil
}

// ChangedLines returns the line numbers head's work changed in file, in the
// file's new version, from `git diff -U0` hunks.
func ChangedLines(ctx context.Context, root, base, head, file string) (map[int]bool, error) {
	lines := map[int]bool{}
	if head == "" {
		return lines, nil
	}
	out, _, code, err := run(ctx, root, "diff", "-U0", "--no-renames", base+"..."+head, "--", file)
	if err != nil || code != 0 {
		return lines, err
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		// @@ -a,b +c,d @@: lines c to c+d-1 of the new file; d defaults to 1.
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
			continue
		}
		startText, countText, hasCount := strings.Cut(fields[2][1:], ",")
		start, err := strconv.Atoi(startText)
		if err != nil {
			continue
		}
		count := 1
		if hasCount {
			if count, err = strconv.Atoi(countText); err != nil {
				continue
			}
		}
		for n := start; n < start+count; n++ {
			lines[n] = true
		}
	}
	return lines, nil
}

// Log lists head's commits that base does not have, newest first, with the
// files each touched. Fields are split on control characters, so any
// commit subject parses.
func Log(ctx context.Context, root, base, head string) ([]Commit, error) {
	if head == "" {
		return nil, nil
	}
	out, _, code, err := run(ctx, root, "log", "--no-renames", "--name-only", "--format=%x00%H%x1f%s%x1f%aI", base+".."+head)
	if err != nil || code != 0 {
		return nil, err
	}
	var commits []Commit
	for _, record := range strings.Split(out, "\x00") {
		header, files, _ := strings.Cut(record, "\n")
		parts := strings.SplitN(header, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		c := Commit{Hash: parts[0], Message: parts[1], Timestamp: parts[2]}
		for _, f := range strings.Split(files, "\n") {
			if f = strings.TrimSpace(f); f != "" {
				c.Files = append(c.Files, f)
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}
