package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
)

func (a *app) agentPromptCmd() *cobra.Command {
	var user, file string
	cmd := &cobra.Command{Use: "prompt", Short: "Persistent instructions for one team member"}
	cmd.PersistentFlags().StringVar(&user, "user", config.IdentityFromEnv().User, "Member's user (default: TEAM_WORKER_USER)")
	cmd.AddCommand(&cobra.Command{
		Use: "show TEAM NAME", Short: "Print a member's instructions", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt, err := a.client().AgentPrompt(cmd.Context(), args[0], args[1], user)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"team": args[0], "agent": args[1], "user": user, "prompt": prompt}, func(w io.Writer) { fmt.Fprintln(w, prompt) })
			return nil
		},
	})
	set := &cobra.Command{
		Use: "set TEAM NAME [TEXT]", Short: "Replace instructions from TEXT, --file or stdin; empty clears them", Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			var text string
			switch {
			case len(args) == 3:
				text = args[2]
			case cmd.Flags().Changed("file"):
				data, err := os.ReadFile(file)
				if err != nil {
					return a.fail(err)
				}
				text = string(data)
			default:
				data, err := io.ReadAll(a.in)
				if err != nil {
					return a.fail(err)
				}
				text = string(data)
			}
			changed, err := a.client().SetAgentPrompt(cmd.Context(), args[0], args[1], user, strings.TrimRight(text, "\n"))
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"team": args[0], "agent": args[1], "user": user, "changed": changed}, func(w io.Writer) {
				if changed {
					fmt.Fprintln(w, "OK Agent instructions saved; member was notified")
				} else {
					fmt.Fprintln(w, "Agent instructions unchanged")
				}
			})
			return nil
		},
	}
	set.Flags().StringVarP(&file, "file", "f", "", "Read the instructions from a file")
	cmd.AddCommand(set)
	return cmd
}
