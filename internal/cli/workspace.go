package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/model"
)

func (a *app) workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "Agents' git worktrees: list, status, checkpoint, merge, cleanup"}
	cmd.AddCommand(a.workspaceListCmd(), a.workspaceStatusCmd(), a.workspaceCheckpointCmd(),
		a.workspaceMergeCmd(), a.workspaceCleanupCmd(), a.workspaceImportCmd())
	return cmd
}

func repoFlag(cmd *cobra.Command, repo *string) {
	cmd.Flags().StringVar(repo, "repo", "", "Only this repository of the agent's (its path or directory name)")
}

func (a *app) workspaceListCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "list TEAM",
		Short: "List the team's agent worktrees",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, err := a.client().ListWorkspaces(cmd.Context(), args[0], "")
			if err != nil {
				return a.fail(err)
			}
			list := []model.Workspace{}
			for _, ws := range all {
				if repo == "" || ws.RepoRoot == repo || filepath.Base(ws.RepoRoot) == repo {
					list = append(list, ws)
				}
			}
			a.emit(map[string][]model.Workspace{"workspaces": list}, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintf(w, "No active workspaces for team '%s'.\n", args[0])
					return
				}
				tw := newTable(w, "AGENT", "REPO", "BRANCH", "PATH", "CREATED")
				for _, ws := range list {
					row(tw, ws.AgentName, filepath.Base(ws.RepoRoot), ws.BranchName, ws.WorktreePath, formatTimestamp(ws.CreatedAt))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Only worktrees of this repository (its root or directory name)")
	return cmd
}

func (a *app) workspaceStatusCmd() *cobra.Command {
	var in model.WorkspaceRequest
	cmd := &cobra.Command{
		Use:   "status TEAM AGENT",
		Short: "Show the uncommitted changes in an agent's worktrees",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.client().WorkspaceStatus(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(op, func(w io.Writer) {
				for _, r := range op.Repos {
					fmt.Fprintf(w, "Workspace status — %s (branch: %s, %s)\n", args[1], r.Branch, filepath.Base(r.Repo))
					if r.Output == "" {
						fmt.Fprintln(w, "  no uncommitted changes")
					} else {
						fmt.Fprintln(w, r.Output)
					}
				}
			})
			return nil
		},
	}
	repoFlag(cmd, &in.Repo)
	return cmd
}

func (a *app) workspaceCheckpointCmd() *cobra.Command {
	var in model.WorkspaceRequest
	cmd := &cobra.Command{
		Use:   "checkpoint TEAM AGENT",
		Short: "Commit everything in an agent's worktrees",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.client().CheckpointWorkspace(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(op, func(w io.Writer) {
				if op.Status == "checkpoint_created" {
					fmt.Fprintf(w, "OK Checkpoint created for '%s'.\n", args[1])
				} else {
					fmt.Fprintf(w, "No changes to checkpoint for '%s'.\n", args[1])
				}
			})
			return nil
		},
	}
	repoFlag(cmd, &in.Repo)
	cmd.Flags().StringVarP(&in.Message, "message", "m", "", "Commit message")
	return cmd
}

func (a *app) workspaceMergeCmd() *cobra.Command {
	var in model.WorkspaceRequest
	cmd := &cobra.Command{
		Use:   "merge TEAM AGENT",
		Short: "Merge an agent's branches back into their base branches",
		Long: "Checkpoints the agent's worktrees and merges each branch into its base branch (or --target)\n" +
			"with a merge commit. Every repository is tried first: a conflict or uncommitted changes in\n" +
			"any of them leave all of them unmerged. No branch is switched anywhere. After a merge the\n" +
			"worktrees and branches are removed unless --no-cleanup is given.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.client().MergeWorkspace(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(op, func(w io.Writer) {
				if op.Status == "merged" {
					fmt.Fprintf(w, "OK Merged '%s' workspace.\n%s\n", args[1], op.Output)
				} else {
					fmt.Fprintf(w, "Merge failed for '%s':\n%s\n", args[1], op.Output)
				}
				printRepoNotes(w, op.Repos)
			})
			if op.Status != "merged" {
				return exitError{}
			}
			return nil
		},
	}
	repoFlag(cmd, &in.Repo)
	cmd.Flags().StringVar(&in.Target, "target", "", "Target branch (default: each worktree's base branch)")
	cmd.Flags().BoolVar(&in.NoCleanup, "no-cleanup", false, "Keep the worktrees after merging")
	return cmd
}

func (a *app) workspaceCleanupCmd() *cobra.Command {
	var in model.WorkspaceRequest
	cmd := &cobra.Command{
		Use:   "cleanup TEAM",
		Short: "Remove agent worktrees, keeping branches with unmerged commits",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.client().CleanupWorkspaces(cmd.Context(), args[0], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(op, func(w io.Writer) {
				if in.Agent != "" {
					fmt.Fprintf(w, "OK Cleaned up workspace for '%s'.\n", in.Agent)
				} else {
					fmt.Fprintf(w, "OK Cleaned up %d workspace(s) for team '%s'.\n", op.Count, args[0])
				}
				printRepoNotes(w, op.Repos)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&in.Agent, "agent", "a", "", "Agent name (all if omitted)")
	repoFlag(cmd, &in.Repo)
	return cmd
}

func (a *app) workspaceImportCmd() *cobra.Command {
	var in model.WorkspaceRequest
	cmd := &cobra.Command{
		Use:   "import TEAM",
		Short: "Take over the worktrees of a team moved from ClawTeam",
		Long: "Records the worktrees listed in ClawTeam's workspace registry, by default\n" +
			"~/.clawteam/workspaces/TEAM/workspace-registry.json on the daemon's machine. Worktrees that\n" +
			"no longer exist are skipped. Agents spawned again reuse the imported worktrees where they are.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.client().ImportWorkspaces(cmd.Context(), args[0], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(op, func(w io.Writer) {
				fmt.Fprintf(w, "OK Imported %d workspace(s) for team '%s'.\n", op.Count, args[0])
				printRepoNotes(w, op.Repos)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&in.File, "file", "", "ClawTeam workspace-registry.json on the daemon's machine")
	return cmd
}

// printRepoNotes shows what removing each worktree left behind, such as a
// branch kept for its unmerged commits.
func printRepoNotes(w io.Writer, repos []model.WorkspaceRepo) {
	for _, r := range repos {
		if r.Output != "" {
			fmt.Fprintf(w, "  %s: %s\n", filepath.Base(r.Repo), r.Output)
		}
	}
}
