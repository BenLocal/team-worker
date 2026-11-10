package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func taskResultTime(t model.Task) string {
	if t.Result != nil && t.Result.ReportedAt != "" {
		return t.Result.ReportedAt
	}
	return t.UpdatedAt
}

func (a *app) taskResultsCmd() *cobra.Command {
	var owner string
	var all bool
	cmd := &cobra.Command{
		Use:   "results TEAM",
		Short: "Show each owner's latest completed task (use --json for full reports and task notes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, err := a.client().ListTasks(cmd.Context(), args[0], store.TaskFilter{Status: model.StatusCompleted, Owner: owner})
			if err != nil {
				return a.fail(err)
			}
			sort.SliceStable(tasks, func(i, j int) bool { return taskResultTime(tasks[i]) > taskResultTime(tasks[j]) })
			results := make([]model.Task, 0, len(tasks))
			seen := map[string]bool{}
			for _, task := range tasks {
				if !all && seen[task.Owner] {
					continue
				}
				seen[task.Owner] = true
				results = append(results, task)
			}
			a.emit(results, func(w io.Writer) {
				if len(results) == 0 {
					fmt.Fprintln(w, "No completed tasks found")
				}
				for i, task := range results {
					if i > 0 {
						fmt.Fprintln(w)
					}
					name := task.Owner
					if name == "" {
						name = "Unassigned"
					}
					fmt.Fprintf(w, "%s: %s\n  Task: %s\n  Updated: %s\n", name, task.Subject, task.ID, formatTimestamp(taskResultTime(task)))
					if task.Result == nil {
						fmt.Fprintln(w, "  No separate report; use task get or --json for task notes")
						continue
					}
					fmt.Fprintf(w, "  Result: %s\n", task.Result.Summary)
					for _, artifact := range task.Result.Artifacts {
						fmt.Fprintf(w, "  Artifact: %s\n", artifact)
					}
					verification := task.Result.Verification
					if verification == "" {
						verification = "Not reported"
					}
					fmt.Fprintf(w, "  Verification: %s\n", verification)
				}
			})
			return nil
		},
	}
	ownerFlags(cmd, &owner, "Filter by task owner")
	cmd.Flags().BoolVar(&all, "all", false, "Include all completed tasks, newest reports first")
	return cmd
}
