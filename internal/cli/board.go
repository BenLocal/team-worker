package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/board"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func (a *app) boardCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "board", Short: "Team board: members, tasks and messages"}
	cmd.AddCommand(a.boardShowCmd(), a.boardServeCmd(), a.boardOverviewCmd(), a.boardLiveCmd(), a.boardUpdateCmd())
	return cmd
}

func (a *app) boardShowCmd() *cobra.Command {
	var messages int
	cmd := &cobra.Command{
		Use:   "show TEAM",
		Short: "Print the team board",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := a.client().Board(cmd.Context(), args[0], messages)
			if err != nil {
				return a.fail(err)
			}
			a.emit(b, func(w io.Writer) { printBoard(w, b) })
			return nil
		},
	}
	cmd.Flags().IntVarP(&messages, "messages", "m", 10, "Number of recent messages to show")
	return cmd
}

func (a *app) boardOverviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "overview",
		Short: "Summarize every team: leader, members, tasks and waiting messages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			teams, err := a.client().Overview(cmd.Context())
			if err != nil {
				return a.fail(err)
			}
			a.emit(teams, func(w io.Writer) {
				if len(teams) == 0 {
					fmt.Fprintln(w, "No teams found")
					return
				}
				tw := newTable(w, "TEAM", "LEADER", "MEMBERS", "TASKS", "PENDING MSGS")
				for _, t := range teams {
					row(tw, t.Name, t.Leader, fmt.Sprint(t.Members), fmt.Sprint(t.Tasks), fmt.Sprint(t.PendingMessages))
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func (a *app) boardLiveCmd() *cobra.Command {
	var interval time.Duration
	var messages, count int
	cmd := &cobra.Command{
		Use:   "live TEAM",
		Short: "Redraw the team board in the terminal until Ctrl+C",
		Long: "Redraws `board show` every --interval. With --json it prints one board per line\n" +
			"instead of redrawing. --count stops after that many boards.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return a.fail(errors.New("--interval must be positive"))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			for n := 1; ; n++ {
				b, err := a.client().Board(ctx, args[0], messages)
				if ctx.Err() != nil {
					return nil
				}
				if err != nil {
					return a.fail(err)
				}
				if a.jsonOut {
					printJSONLine(a.out, b)
				} else {
					// Home the cursor and clear the screen, then redraw.
					fmt.Fprint(a.out, "\x1b[H\x1b[2J")
					fmt.Fprintf(a.out, "%s  (every %s, Ctrl+C to stop)\n\n", time.Now().Format("15:04:05"), interval)
					printBoard(a.out, b)
				}
				if count > 0 && n >= count {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", 2*time.Second, "Refresh interval")
	cmd.Flags().IntVarP(&messages, "messages", "m", 10, "Number of recent messages to show")
	cmd.Flags().IntVar(&count, "count", 0, "Stop after this many refreshes (0: until Ctrl+C)")
	return cmd
}

// boardUpdateCmd keeps ClawTeam's command: the board is derived from tasks
// and messages, so there is nothing to update.
func (a *app) boardUpdateCmd() *cobra.Command {
	var agentName string
	cmd := &cobra.Command{
		Use:   "update TEAM",
		Short: "Compatibility alias: the board always reflects tasks and messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.client().Board(cmd.Context(), args[0], 0); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"status": "up_to_date", "team": args[0], "agent": nullable(agentName),
				"note": "Board state is derived automatically from task and inbox updates."}, func(w io.Writer) {
				fmt.Fprintln(w, "OK The board is derived from tasks and inbox messages; change them with `task create/update`,\n"+
					"then view it with `board show` or `board live`.")
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agentName, "agent", "a", "", "Accepted for compatibility; the board is team-wide")
	return cmd
}

// nullable is nil for an empty string, so JSON shows null like ClawTeam.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func printBoard(w io.Writer, b model.Board) {
	s := b.Stats
	fmt.Fprintf(w, "Team %s — %d/%d tasks done, %d in progress, %d pending, %d blocked\n\n",
		b.Team.Name, s.Completed, s.Total, s.InProgress, s.Pending, s.Blocked)

	tw := newTable(w, "MEMBER", "TYPE", "STATE", "UNREAD")
	for _, m := range b.Members {
		state := "not spawned"
		if m.Agent != nil {
			state = aliveLabel(*m.Agent)
		}
		name := m.Name
		if m.AgentID == b.Team.LeadAgentID {
			name += " (leader)"
		}
		row(tw, name, m.AgentType, state, fmt.Sprint(m.InboxCount))
	}
	tw.Flush()

	for _, status := range []model.TaskStatus{model.StatusInProgress, model.StatusPending,
		model.StatusBlocked, model.StatusCompleted} {
		var lines []string
		for _, t := range b.Tasks {
			if t.Status != status {
				continue
			}
			line := fmt.Sprintf("  %s  %s", t.ID, t.Subject)
			if t.Owner != "" {
				line += "  @" + t.Owner
			}
			if t.Priority != model.PriorityMedium {
				line += "  [" + string(t.Priority) + "]"
			}
			if len(t.BlockedBy) > 0 {
				line += "  waits for " + strings.Join(t.BlockedBy, ",")
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			fmt.Fprintf(w, "\n%s (%d)\n%s\n", strings.ToUpper(string(status)), len(lines), strings.Join(lines, "\n"))
		}
	}

	if len(b.Messages) > 0 {
		fmt.Fprintln(w, "\nRECENT MESSAGES")
		for _, m := range b.Messages {
			fmt.Fprintf(w, "  [%s] %s → %s: %s\n", formatTimestamp(m.Timestamp), m.From, m.To,
				truncate(strings.ReplaceAll(m.Content, "\n", " "), 100))
		}
	}
}

func (a *app) boardServeCmd() *cobra.Command {
	var host string
	var port int
	var linkPairs []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web board from this machine (read-only)",
		Long: "Serves the web board for the daemon this CLI reaches. team-workerd can serve the same\n" +
			"board itself (--board); this command is for viewing a remote daemon from another\n" +
			"machine. It is a client: it has no live terminals and shows each agent's screen as\n" +
			"the daemon reads it. Sign in with the daemon's API key.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			links, err := board.ParseLinks(linkPairs)
			if err != nil {
				return a.fail(err)
			}
			c := a.client()
			key, err := c.BoardKey(cmd.Context())
			if err != nil {
				return a.fail(err)
			}
			if _, err := c.ListTeams(cmd.Context()); err != nil {
				return a.fail(err)
			}
			addr := net.JoinHostPort(host, fmt.Sprint(port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return a.fail(err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			// A board started by the CLI is a client: it has no live terminals
			// and shows each agent's screen from the daemon.
			protected, err := board.Site(c, key, config.WebBasePath(), links, nil)
			if err != nil {
				ln.Close()
				return a.fail(err)
			}
			srv := &http.Server{Handler: protected, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-ctx.Done()
				shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				srv.Shutdown(shutdown)
			}()
			fmt.Fprintf(a.errOut, "Board: http://%s (Ctrl+C to stop)\n", ln.Addr())
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return a.fail(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Address to listen on")
	cmd.Flags().IntVarP(&port, "port", "p", 8080, "Port to listen on")
	cmd.Flags().StringArrayVar(&linkPairs, "link", nil,
		"Link to another service in the page header, NAME=URL (repeatable), e.g. --link Docs=https://example.com")
	return cmd
}
