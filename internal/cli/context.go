package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) contextCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Git context across agent worktrees: diffs, file owners, conflicts, log"}
	cmd.AddCommand(a.contextDiffCmd(), a.contextFilesCmd(), a.contextConflictsCmd(), a.contextLogCmd(), a.contextInjectCmd())
	return cmd
}

func contextRepoFlag(cmd *cobra.Command, repo *string) {
	cmd.Flags().StringVar(repo, "repo", "", "Only this repository (its path or directory name)")
}

func (a *app) contextDiffCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "diff TEAM AGENT",
		Short: "Show what an agent's work changes relative to its base branch",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.client().ContextDiff(cmd.Context(), args[0], args[1], repo)
			if err != nil {
				return a.fail(err)
			}
			a.emit(d, func(w io.Writer) {
				fmt.Fprintln(w, d.Summary)
				if d.DiffStat != "" {
					fmt.Fprintln(w, d.DiffStat)
				}
			})
			return nil
		},
	}
	contextRepoFlag(cmd, &repo)
	return cmd
}

func (a *app) contextFilesCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "files TEAM",
		Short: "Show which agents changed which files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owners, err := a.client().ContextFiles(cmd.Context(), args[0], repo)
			if err != nil {
				return a.fail(err)
			}
			a.emit(owners, func(w io.Writer) {
				if len(owners) == 0 {
					fmt.Fprintln(w, "No modified files found.")
					return
				}
				files := make([]string, 0, len(owners))
				for f := range owners {
					files = append(files, f)
				}
				sort.Strings(files)
				tw := newTable(w, "FILE", "AGENTS")
				for _, f := range files {
					row(tw, f, strings.Join(owners[f], ", "))
				}
				tw.Flush()
			})
			return nil
		},
	}
	contextRepoFlag(cmd, &repo)
	return cmd
}

func (a *app) contextConflictsCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "conflicts TEAM",
		Short: "Show files several agents changed: high when the same lines, else medium",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := a.client().ContextConflicts(cmd.Context(), args[0], repo)
			if err != nil {
				return a.fail(err)
			}
			a.emit(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No overlaps detected.")
					return
				}
				tw := newTable(w, "FILE", "AGENTS", "SEVERITY")
				for _, o := range list {
					row(tw, o.File, strings.Join(o.Agents, ", "), o.Severity)
				}
				tw.Flush()
			})
			return nil
		},
	}
	contextRepoFlag(cmd, &repo)
	return cmd
}

func (a *app) contextLogCmd() *cobra.Command {
	var repo string
	var limit int
	cmd := &cobra.Command{
		Use:   "log TEAM",
		Short: "Show the agents' commits across their branches, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := a.client().ContextLog(cmd.Context(), args[0], repo, limit)
			if err != nil {
				return a.fail(err)
			}
			a.emit(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No commits found.")
					return
				}
				for _, c := range list {
					fmt.Fprintf(w, "%s %s %s %s\n", formatTimestamp(c.Timestamp), c.Agent, c.Hash[:min(len(c.Hash), 8)], c.Message)
					for _, f := range c.Files {
						fmt.Fprintf(w, "    %s\n", f)
					}
				}
			})
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Max entries")
	contextRepoFlag(cmd, &repo)
	return cmd
}

func (a *app) contextInjectCmd() *cobra.Command {
	var repo string
	var send bool
	cmd := &cobra.Command{
		Use:   "inject TEAM AGENT",
		Short: "Write the git context block for an agent's prompt",
		Long: "Writes what the agent should know about the other agents' work: files they also changed,\n" +
			"their recent commits on those files, and the work its tasks wait for. spawn puts it in the\n" +
			"first prompt; --send also delivers it to the agent now as a message.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			text, err := c.ContextInject(cmd.Context(), args[0], args[1], repo)
			if err != nil {
				return a.fail(err)
			}
			if send {
				if _, err := c.Send(cmd.Context(), args[0], store.SendInput{
					From: config.IdentityFromEnv().AgentName, To: args[1], Content: text}); err != nil {
					return a.fail(err)
				}
			}
			a.emit(map[string]string{"context": text}, func(w io.Writer) { fmt.Fprintln(w, text) })
			return nil
		},
	}
	contextRepoFlag(cmd, &repo)
	cmd.Flags().BoolVar(&send, "send", false, "Also send the block to the agent as a message")
	return cmd
}
