package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Task management"}
	cmd.AddCommand(a.taskCreateCmd(), a.taskGetCmd(), a.taskUpdateCmd(), a.taskListCmd(),
		a.taskStatsCmd(), a.taskWaitCmd())
	return cmd
}

// ownerFlags registers --owner/-o and its --agent/-a alias on one variable.
func ownerFlags(cmd *cobra.Command, target *string, usage string) {
	cmd.Flags().StringVarP(target, "owner", "o", "", usage)
	cmd.Flags().StringVarP(target, "agent", "a", "", usage+" (alias of --owner)")
}

func ownerChanged(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("owner") || cmd.Flags().Changed("agent")
}

func (a *app) taskCreateCmd() *cobra.Command {
	var description, owner, priority, blocks, blockedBy string
	cmd := &cobra.Command{
		Use:   "create TEAM SUBJECT",
		Short: "Create a new task",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := model.ParsePriority(priority)
			if err != nil {
				return a.fail(err)
			}
			task, err := a.client().CreateTask(cmd.Context(), args[0], store.CreateTaskInput{
				Subject:     args[1],
				Description: description,
				Owner:       owner,
				Priority:    p,
				Blocks:      splitList(blocks),
				BlockedBy:   splitList(blockedBy),
			})
			if err != nil {
				return a.fail(err)
			}
			a.emit(task, func(w io.Writer) {
				fmt.Fprintf(w, "OK Task created: %s\n", task.ID)
				fmt.Fprintf(w, "  Subject: %s\n", task.Subject)
				fmt.Fprintf(w, "  Status: %s\n", task.Status)
				fmt.Fprintf(w, "  Priority: %s\n", task.Priority)
				if task.Owner != "" {
					fmt.Fprintf(w, "  Owner: %s\n", task.Owner)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&description, "description", "d", "", "Task description")
	ownerFlags(cmd, &owner, "Owner agent name")
	cmd.Flags().StringVarP(&priority, "priority", "p", "medium", "Task priority: low, medium, high, urgent")
	cmd.Flags().StringVar(&blocks, "blocks", "", "Comma-separated task IDs this blocks")
	cmd.Flags().StringVar(&blockedBy, "blocked-by", "", "Comma-separated task IDs this is blocked by")
	return cmd
}

func (a *app) taskGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get TEAM TASK_ID",
		Short: "Get a single task",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := a.client().GetTask(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			a.emit(task, func(w io.Writer) {
				fmt.Fprintf(w, "Task: %s\n", task.ID)
				fmt.Fprintf(w, "  Subject: %s\n", task.Subject)
				fmt.Fprintf(w, "  Status: %s\n", task.Status)
				fmt.Fprintf(w, "  Priority: %s\n", task.Priority)
				if task.Owner != "" {
					fmt.Fprintf(w, "  Owner: %s\n", task.Owner)
				}
				if task.LockedBy != "" {
					fmt.Fprintf(w, "  Locked by: %s (since %s)\n", task.LockedBy, formatTimestamp(task.LockedAt))
				}
				if task.Description != "" {
					fmt.Fprintf(w, "  Description: %s\n", task.Description)
				}
				if len(task.Blocks) > 0 {
					fmt.Fprintf(w, "  Blocks: %s\n", strings.Join(task.Blocks, ", "))
				}
				if len(task.BlockedBy) > 0 {
					fmt.Fprintf(w, "  Blocked by: %s\n", strings.Join(task.BlockedBy, ", "))
				}
			})
			return nil
		},
	}
}

func (a *app) taskUpdateCmd() *cobra.Command {
	var status, owner, subject, description, priority, addBlocks, addBlockedBy string
	var force bool
	cmd := &cobra.Command{
		Use:   "update TEAM TASK_ID",
		Short: "Update a task",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := store.UpdateTaskInput{
				AddBlocks:    splitList(addBlocks),
				AddBlockedBy: splitList(addBlockedBy),
				Caller:       config.IdentityFromEnv().AgentName,
				Force:        force,
			}
			if cmd.Flags().Changed("status") {
				s, err := model.ParseTaskStatus(status)
				if err != nil {
					return a.fail(err)
				}
				in.Status = &s
			}
			if cmd.Flags().Changed("priority") {
				p, err := model.ParsePriority(priority)
				if err != nil {
					return a.fail(err)
				}
				in.Priority = &p
			}
			if ownerChanged(cmd) {
				in.Owner = &owner
			}
			if cmd.Flags().Changed("subject") {
				in.Subject = &subject
			}
			if cmd.Flags().Changed("description") {
				in.Description = &description
			}
			task, err := a.client().UpdateTask(cmd.Context(), args[0], args[1], in)
			if err != nil {
				if client.IsConflict(err) && !a.jsonOut {
					return a.fail(fmt.Errorf("Lock conflict: %w", err))
				}
				return a.fail(err)
			}
			a.emit(task, func(w io.Writer) { fmt.Fprintf(w, "OK Task %s updated\n", task.ID) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&status, "status", "s", "", "New status: pending, in_progress, completed, blocked")
	ownerFlags(cmd, &owner, "New owner")
	cmd.Flags().StringVar(&subject, "subject", "", "New subject")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description")
	cmd.Flags().StringVarP(&priority, "priority", "p", "", "New priority: low, medium, high, urgent")
	cmd.Flags().StringVar(&addBlocks, "add-blocks", "", "Comma-separated task IDs this blocks")
	cmd.Flags().StringVar(&addBlockedBy, "add-blocked-by", "", "Comma-separated task IDs blocking this")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force override task lock")
	return cmd
}

func (a *app) taskListCmd() *cobra.Command {
	var status, owner, priority string
	var sortPriority bool
	cmd := &cobra.Command{
		Use:   "list TEAM",
		Short: "List tasks for a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := store.TaskFilter{Owner: owner, SortPriority: sortPriority}
			if status != "" {
				s, err := model.ParseTaskStatus(status)
				if err != nil {
					return a.fail(err)
				}
				f.Status = s
			}
			if priority != "" {
				p, err := model.ParsePriority(priority)
				if err != nil {
					return a.fail(err)
				}
				f.Priority = p
			}
			tasks, err := a.client().ListTasks(cmd.Context(), args[0], f)
			if err != nil {
				return a.fail(err)
			}
			a.emit(tasks, func(w io.Writer) {
				if len(tasks) == 0 {
					fmt.Fprintln(w, "No tasks found")
					return
				}
				fmt.Fprintf(w, "Tasks - %s\n", args[0])
				tw := newTable(w, "ID", "SUBJECT", "STATUS", "PRIORITY", "OWNER", "LOCK", "BLOCKED BY")
				for _, t := range tasks {
					row(tw, t.ID, t.Subject, string(t.Status), string(t.Priority), t.Owner, t.LockedBy,
						strings.Join(t.BlockedBy, ", "))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&status, "status", "s", "", "Filter by status")
	ownerFlags(cmd, &owner, "Filter by owner")
	cmd.Flags().StringVarP(&priority, "priority", "p", "", "Filter by priority: low, medium, high, urgent")
	cmd.Flags().BoolVar(&sortPriority, "sort-priority", false, "Sort by priority (urgent first)")
	return cmd
}

func (a *app) taskStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats TEAM",
		Short: "Show task timing statistics for a team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.client().TaskStats(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(st, func(w io.Writer) {
				fmt.Fprintf(w, "Task Stats - %s\n", args[0])
				tw := newTable(w, "METRIC", "VALUE")
				row(tw, "Total tasks", fmt.Sprint(st.Total))
				row(tw, "Completed", fmt.Sprint(st.Completed))
				row(tw, "In progress", fmt.Sprint(st.InProgress))
				row(tw, "Pending", fmt.Sprint(st.Pending))
				row(tw, "Blocked", fmt.Sprint(st.Blocked))
				row(tw, "With timing data", fmt.Sprint(st.TimedCompleted))
				row(tw, "Avg completion time", formatDuration(st.AvgDurationSeconds))
				tw.Flush()
			})
			return nil
		},
	}
}

func formatDuration(secs float64) string {
	switch {
	case secs <= 0:
		return "-"
	case secs < 60:
		return fmt.Sprintf("%.1fs", secs)
	case secs < 3600:
		return fmt.Sprintf("%.1fm", secs/60)
	}
	return fmt.Sprintf("%.1fh", secs/3600)
}

type taskSummary struct {
	ID      string           `json:"id"`
	Subject string           `json:"subject"`
	Status  model.TaskStatus `json:"status"`
	Owner   string           `json:"owner"`
}

type taskCounts struct {
	total, completed, inProgress, pending, blocked int
}

func countTasks(tasks []model.Task) taskCounts {
	c := taskCounts{total: len(tasks)}
	for _, t := range tasks {
		switch t.Status {
		case model.StatusCompleted:
			c.completed++
		case model.StatusInProgress:
			c.inProgress++
		case model.StatusPending:
			c.pending++
		case model.StatusBlocked:
			c.blocked++
		}
	}
	return c
}

func (a *app) taskWaitCmd() *cobra.Command {
	var agent string
	var pollInterval, timeout float64
	cmd := &cobra.Command{
		Use:   "wait TEAM",
		Short: "Block until all tasks in a team are completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			team := args[0]
			c := a.client()
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			inbox, user := agent, config.IdentityFromEnv().User
			if inbox == "" {
				leader, err := c.Leader(ctx, team)
				if err != nil {
					return a.fail(err)
				}
				inbox, user = leader.Name, leader.User
			}
			poll := time.Duration(pollInterval * float64(time.Second))
			if poll <= 0 {
				poll = 5 * time.Second
			}
			if !a.jsonOut {
				timeoutStr := "none"
				if timeout > 0 {
					timeoutStr = fmt.Sprintf("%.0fs", timeout)
				}
				fmt.Fprintf(a.out, "Waiting for all tasks in team '%s' to complete...\n", team)
				fmt.Fprintf(a.out, "  Agent inbox: %s  |  Poll interval: %gs  |  Timeout: %s\n\n",
					inbox, pollInterval, timeoutStr)
			}

			start := time.Now()
			received := 0
			drain := func(wait time.Duration) error {
				msgs, err := c.Receive(ctx, team, inbox, user, 50, wait)
				if err != nil {
					return err
				}
				for _, m := range msgs {
					received++
					a.printWaitMessage(m)
				}
				return nil
			}

			status, lastSummary := "interrupted", ""
			var tasks []model.Task
			for {
				var err error
				tasks, err = c.ListTasks(ctx, team, store.TaskFilter{})
				if ctx.Err() != nil {
					break
				}
				if err != nil {
					return a.fail(err)
				}
				n := countTasks(tasks)
				if summary := fmt.Sprintf("%d/%d", n.completed, n.total); summary != lastSummary {
					lastSummary = summary
					a.printProgress(n)
				}
				if n.completed == n.total {
					if err := drain(0); err != nil && ctx.Err() == nil {
						return a.fail(err)
					}
					status = "completed"
					break
				}
				wait := poll
				if timeout > 0 {
					remaining := time.Duration(timeout*float64(time.Second)) - time.Since(start)
					if remaining <= 0 {
						status = "timeout"
						break
					}
					wait = min(wait, remaining)
				}
				// Long-poll the inbox for up to one poll interval; messages wake it early.
				if err := drain(wait); err != nil {
					if ctx.Err() != nil {
						break
					}
					return a.fail(err)
				}
			}
			if status == "interrupted" {
				// Report the final state even though the wait context is cancelled.
				if latest, err := c.ListTasks(context.WithoutCancel(ctx), team, store.TaskFilter{}); err == nil {
					tasks = latest
				}
			}
			a.printWaitResult(status, time.Since(start), tasks, received)
			if status != "completed" {
				return exitError{}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Agent inbox to monitor (default: team leader)")
	cmd.Flags().Float64VarP(&pollInterval, "poll-interval", "p", 5, "Seconds between task checks")
	cmd.Flags().Float64VarP(&timeout, "timeout", "t", 0, "Max seconds to wait (default: no limit)")
	return cmd
}

func (a *app) printWaitMessage(m model.Message) {
	if a.jsonOut {
		printJSONLine(a.out, map[string]string{
			"event": "message", "from": m.From, "content": m.Content, "timestamp": m.Timestamp,
		})
		return
	}
	ts := m.Timestamp
	if i := strings.Index(ts, "T"); i >= 0 && len(ts) >= i+9 {
		ts = ts[i+1 : i+9]
	}
	fmt.Fprintf(a.out, "  %s  message from=%s: %s\n", ts, m.From, m.Content)
}

func (a *app) printProgress(n taskCounts) {
	if a.jsonOut {
		printJSONLine(a.out, map[string]any{
			"event": "progress", "completed": n.completed, "total": n.total,
			"in_progress": n.inProgress, "pending": n.pending, "blocked": n.blocked,
		})
		return
	}
	fmt.Fprintf(a.out, "  %d/%d tasks completed  (%d in progress, %d pending, %d blocked)\n",
		n.completed, n.total, n.inProgress, n.pending, n.blocked)
}

func (a *app) printWaitResult(status string, elapsed time.Duration, tasks []model.Task, received int) {
	n := countTasks(tasks)
	secs := elapsed.Seconds()
	if a.jsonOut {
		details := make([]taskSummary, len(tasks))
		for i, t := range tasks {
			details[i] = taskSummary{ID: t.ID, Subject: t.Subject, Status: t.Status, Owner: t.Owner}
		}
		printJSONLine(a.out, map[string]any{
			"event": "result", "status": status, "elapsed": math.Round(secs*10) / 10,
			"total": n.total, "completed": n.completed, "in_progress": n.inProgress,
			"pending": n.pending, "blocked": n.blocked, "messages_received": received,
			"task_details": details,
		})
		return
	}
	fmt.Fprintln(a.out)
	switch status {
	case "completed":
		fmt.Fprintf(a.out, "All %d tasks completed! (%.1fs, %d messages)\n", n.total, secs, received)
		return
	case "timeout":
		fmt.Fprintf(a.out, "Timeout after %.1fs. %d/%d completed.\n", secs, n.completed, n.total)
	default:
		fmt.Fprintf(a.out, "Interrupted after %.1fs. %d/%d completed.\n", secs, n.completed, n.total)
	}
	printed := false
	for _, t := range tasks {
		if t.Status == model.StatusCompleted {
			continue
		}
		if !printed {
			fmt.Fprintln(a.out, "  Incomplete tasks:")
			printed = true
		}
		owner := t.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Fprintf(a.out, "    [%s] %s  %s  (owner: %s)\n", t.Status, t.ID, t.Subject, owner)
	}
}
