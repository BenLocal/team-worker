package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/model"
)

// snapshotCmds are ClawTeam's team snapshot commands. Snapshots live in the
// daemon's database; a restore needs the team's agents stopped.
func (a *app) snapshotCmds() []*cobra.Command {
	var tag string
	snapshot := &cobra.Command{
		Use:   "snapshot TEAM",
		Short: "Save the team's configuration, tasks, messages, events, sessions, plans and costs",
		Long: "Saves the team's configuration and prompts, members, tasks and dependencies, unread messages,\n" +
			"events, sessions, plans, costs and budget in the daemon's database. It is consistent while\n" +
			"agents work. Agent processes, git worktrees and harness runs are not saved.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := a.client().CreateSnapshot(cmd.Context(), args[0], tag)
			if err != nil {
				return a.fail(err)
			}
			a.emit(meta, func(w io.Writer) {
				fmt.Fprintf(w, "OK Snapshot %s created\n", meta.ID)
				fmt.Fprintf(w, "  %d tasks, %d events, %d sessions, %d cost events, %d unread messages\n",
					meta.TaskCount, meta.EventCount, meta.SessionCount, meta.CostEventCount, meta.MessageCount)
			})
			return nil
		},
	}
	snapshot.Flags().StringVarP(&tag, "tag", "t", "", "Label for this snapshot")

	list := &cobra.Command{
		Use:   "snapshots TEAM",
		Short: "List the team's snapshots, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snaps, err := a.client().ListSnapshots(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(snaps, func(w io.Writer) {
				if len(snaps) == 0 {
					fmt.Fprintln(w, "No snapshots found")
					return
				}
				tw := newTable(w, "ID", "TAG", "MEMBERS", "TASKS", "EVENTS", "CREATED")
				for _, s := range snaps {
					row(tw, s.ID, s.Tag, fmt.Sprint(s.MemberCount), fmt.Sprint(s.TaskCount), fmt.Sprint(s.EventCount),
						formatTimestamp(s.CreatedAt))
				}
				tw.Flush()
			})
			return nil
		},
	}

	var dryRun, force bool
	restore := &cobra.Command{
		Use:   "restore TEAM SNAPSHOT_ID",
		Short: "Replace the team's state with a snapshot (stop its agents first)",
		Long: "Replaces the team's configuration, members, tasks, unread messages, events, sessions, plans\n" +
			"and costs with the snapshot's, in one transaction. A deleted team is created again. It is\n" +
			"refused while any of the team's agents runs; stop them with `agent stop` first.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			sum, err := c.RestoreSnapshot(cmd.Context(), args[0], args[1], true)
			if err != nil {
				return a.fail(err)
			}
			if dryRun {
				a.emit(sum, func(w io.Writer) {
					fmt.Fprint(w, "Dry run. Would restore: ")
					printRestoreCounts(w, sum)
				})
				return nil
			}
			if !force && !a.jsonOut {
				fmt.Fprint(a.errOut, "Will restore: ")
				printRestoreCounts(a.errOut, sum)
				if !a.confirm("Replace the team's current state?") {
					return a.fail(errors.New("aborted"))
				}
			}
			if sum, err = c.RestoreSnapshot(cmd.Context(), args[0], args[1], false); err != nil {
				return a.fail(err)
			}
			a.emit(sum, func(w io.Writer) { fmt.Fprintf(w, "OK Restored from snapshot %s\n", args[1]) })
			return nil
		},
	}
	restore.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be restored without writing")
	restore.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	del := &cobra.Command{
		Use:   "snapshot-delete TEAM SNAPSHOT_ID",
		Short: "Delete a snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.client().DeleteSnapshot(cmd.Context(), args[0], args[1]); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "deleted", "id": args[1]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Snapshot '%s' deleted\n", args[1])
			})
			return nil
		},
	}
	return []*cobra.Command{snapshot, list, restore, del}
}

func printRestoreCounts(w io.Writer, s model.RestoreSummary) {
	fmt.Fprintf(w, "%d members, %d tasks, %d events, %d sessions, %d plans, %d costs, %d inbox messages\n",
		s.Members, s.Tasks, s.Events, s.Sessions, s.Plans, s.Costs, s.Inboxes)
}
