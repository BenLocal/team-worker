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
