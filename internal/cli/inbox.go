package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

func (a *app) inboxCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "inbox", Short: "Agent messaging"}
	cmd.AddCommand(a.inboxSendCmd(), a.inboxBroadcastCmd(), a.inboxReceiveCmd(), a.inboxPeekCmd(),
		a.inboxLogCmd(), a.inboxWatchCmd())
	return cmd
}

func (a *app) inboxSendCmd() *cobra.Command {
	var key, msgType, from string
	cmd := &cobra.Command{
		Use:   "send TEAM TO [CONTENT]",
		Short: "Send a point-to-point message (content from stdin when omitted)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			mt, err := model.ParseMessageType(msgType)
			if err != nil {
				return a.fail(err)
			}
			var content string
			if len(args) == 3 {
				content = args[2]
			} else {
				data, err := io.ReadAll(a.in)
				if err != nil {
					return a.fail(err)
				}
				content = strings.TrimSuffix(string(data), "\n")
			}
			if from == "" {
				from = config.IdentityFromEnv().AgentName
			}
			msg, err := a.client().Send(cmd.Context(), args[0], store.SendInput{
				From: from, To: args[1], Content: content, Type: mt, Key: key,
			})
			if err != nil {
				return a.fail(err)
			}
			a.emit(msg, func(w io.Writer) { fmt.Fprintf(w, "OK Message sent to '%s'\n", args[1]) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&key, "key", "k", "", "Optional routing key")
	cmd.Flags().StringVar(&msgType, "type", "message", "Message type")
	cmd.Flags().StringVarP(&from, "from", "f", "", "Override sender name (default: from env identity)")
	return cmd
}

func (a *app) inboxBroadcastCmd() *cobra.Command {
	var key, msgType, from string
	cmd := &cobra.Command{
		Use:   "broadcast TEAM CONTENT",
		Short: "Broadcast a message to all team members",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mt, err := model.ParseMessageType(msgType)
			if err != nil {
				return a.fail(err)
			}
			if from == "" {
				from = config.IdentityFromEnv().AgentName
			}
			msgs, err := a.client().Broadcast(cmd.Context(), args[0], store.BroadcastInput{
				From: from, Content: args[1], Type: mt, Key: key,
			})
			if err != nil {
				return a.fail(err)
			}
			recipients := make([]string, len(msgs))
			for i, m := range msgs {
				recipients[i] = m.To
			}
			data := map[string]any{"count": len(msgs), "recipients": recipients}
			a.emit(data, func(w io.Writer) { fmt.Fprintf(w, "OK Broadcast to %d agents\n", len(msgs)) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&key, "key", "k", "", "Optional routing key")
	cmd.Flags().StringVar(&msgType, "type", "broadcast", "Message type")
	cmd.Flags().StringVarP(&from, "from", "f", "", "Override sender name (default: from env identity)")
	return cmd
}

// inboxOwner resolves the agent whose inbox a command reads.
func inboxOwner(agent string) (string, string) {
	id := config.IdentityFromEnv()
	if agent == "" {
		agent = id.AgentName
	}
	return agent, id.User
}

func (a *app) inboxReceiveCmd() *cobra.Command {
	var agent string
	var limit int
	var wait float64
	cmd := &cobra.Command{
		Use:   "receive TEAM",
		Short: "Receive and consume messages from inbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, user := inboxOwner(agent)
			msgs, err := a.client().Receive(cmd.Context(), args[0], name, user, limit,
				time.Duration(wait*float64(time.Second)))
			if err != nil {
				return a.fail(err)
			}
			a.emit(msgs, func(w io.Writer) {
				if len(msgs) == 0 {
					fmt.Fprintln(w, "No messages")
					return
				}
				for _, m := range msgs {
					fmt.Fprintf(w, "[%s] %s from=%s : %s\n", formatTimestamp(m.Timestamp), m.Type, m.From, m.Content)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Agent name (default: from env)")
	cmd.Flags().IntVarP(&limit, "limit", "l", 10, "Max messages to receive")
	cmd.Flags().Float64VarP(&wait, "wait", "w", 0, "Seconds to wait for a message when the inbox is empty")
	return cmd
}

func (a *app) inboxPeekCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "peek TEAM",
		Short: "Peek at messages without consuming them",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, user := inboxOwner(agent)
			msgs, err := a.client().Peek(cmd.Context(), args[0], name, user)
			if err != nil {
				return a.fail(err)
			}
			data := map[string]any{"count": len(msgs), "messages": msgs}
			a.emit(data, func(w io.Writer) {
				fmt.Fprintf(w, "Pending messages: %d\n", len(msgs))
				for _, m := range msgs {
					fmt.Fprintf(w, "  [%s] %s from=%s : %s\n", formatTimestamp(m.Timestamp), m.Type, m.From,
						truncate(m.Content, 80))
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Agent name (default: from env)")
	return cmd
}

func (a *app) inboxLogCmd() *cobra.Command {
	var agent string
	var limit int
	cmd := &cobra.Command{
		Use:   "log TEAM",
		Short: "View message history; does not consume messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msgs, err := a.client().MessageLog(cmd.Context(), args[0], limit, agent)
			if err != nil {
				return a.fail(err)
			}
			data := map[string]any{"count": len(msgs), "messages": msgs}
			a.emit(data, func(w io.Writer) {
				fmt.Fprintf(w, "Message history: %d message(s)\n", len(msgs))
				for _, m := range msgs {
					to := m.To
					if to == "" {
						to = "all"
					}
					fmt.Fprintf(w, "  [%s] %s → %s (%s): %s\n", formatTimestamp(m.Timestamp), m.From, to, m.Type,
						truncate(m.Content, 120))
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Filter by sender agent name")
	cmd.Flags().IntVarP(&limit, "limit", "l", 50, "Max messages to show")
	return cmd
}

func (a *app) inboxWatchCmd() *cobra.Command {
	var agent, execCmd string
	cmd := &cobra.Command{
		Use:   "watch TEAM",
		Short: "Watch inbox for new messages (blocking, Ctrl+C to stop)",
		Long: "Watch inbox for new messages. With --exec, runs a shell command for each message;\n" +
			"message data is passed in TEAM_WORKER_MSG_FROM, TEAM_WORKER_MSG_TO, TEAM_WORKER_MSG_CONTENT,\n" +
			"TEAM_WORKER_MSG_TYPE, TEAM_WORKER_MSG_TIMESTAMP and TEAM_WORKER_MSG_JSON.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			team := args[0]
			name, user := inboxOwner(agent)
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if !a.jsonOut {
				fmt.Fprintf(a.errOut, "Watching inbox for '%s' in team '%s'... (Ctrl+C to stop)\n", name, team)
			}
			c := a.client()
			for ctx.Err() == nil {
				msgs, err := c.Receive(ctx, team, name, user, 10, 30*time.Second)
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					if client.IsUnavailable(err) {
						// The daemon restarted; the next call starts it again if needed.
						select {
						case <-ctx.Done():
						case <-time.After(time.Second):
						}
						continue
					}
					return a.fail(err)
				}
				for _, m := range msgs {
					a.printWatched(m)
					if execCmd != "" {
						a.runCallback(execCmd, m)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Agent name (default: from env)")
	cmd.Flags().StringVarP(&execCmd, "exec", "e", "", "Shell command to run for each new message")
	return cmd
}

func (a *app) printWatched(m model.Message) {
	if a.jsonOut {
		printJSONLine(a.out, m)
		return
	}
	fmt.Fprintf(a.out, "[%s] %s from=%s : %s\n", formatTimestamp(m.Timestamp), m.Type, m.From, m.Content)
}

func (a *app) runCallback(command string, m model.Message) {
	body, _ := json.Marshal(m)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"TEAM_WORKER_MSG_FROM="+m.From,
		"TEAM_WORKER_MSG_TO="+m.To,
		"TEAM_WORKER_MSG_CONTENT="+m.Content,
		"TEAM_WORKER_MSG_TYPE="+string(m.Type),
		"TEAM_WORKER_MSG_TIMESTAMP="+m.Timestamp,
		"TEAM_WORKER_MSG_JSON="+string(body),
	)
	cmd.Stdout, cmd.Stderr = a.out, a.errOut
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(a.errOut, "[warn] exec callback failed: %v\n", err)
	}
}
