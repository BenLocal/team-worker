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
	"github.com/benlocal/team-worker/internal/model"
)

func (a *app) boardCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "board", Short: "Team board: members, tasks and messages"}
	cmd.AddCommand(a.boardShowCmd(), a.boardServeCmd())
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
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web board (read-only, with live agent terminals)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := a.client()
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
			srv := &http.Server{Handler: board.Handler(c), ReadHeaderTimeout: 10 * time.Second}
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
	return cmd
}
