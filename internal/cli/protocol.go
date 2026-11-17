package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) agentProtocolCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "protocol", Short: "Read and acknowledge coordination protocol revisions"}
	for _, action := range []string{"show", "ack"} {
		var runID, version string
		sub := &cobra.Command{Use: action + " TEAM NAME", Short: map[string]string{"show": "Read current role, team and coordination context", "ack": "Declare reading the current revision for this run"}[action], Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			if a.remote != "" && !cmd.Flags().Changed("run-id") {
				runID = ""
			}
			c := a.client()
			var in *model.ProtocolRequest
			if runID != "" {
				if action == "show" {
					current, err := c.AgentProtocol(cmd.Context(), args[0], args[1], nil)
					if err != nil {
						return a.fail(err)
					}
					version = current.Version
				}
				in = &model.ProtocolRequest{Action: map[string]string{"show": "read", "ack": "ack"}[action], RunID: runID, Version: version}
			} else if action == "ack" {
				return a.fail(fmt.Errorf("--run-id is required"))
			}
			if action == "ack" && version == "" {
				return a.fail(fmt.Errorf("--version is required"))
			}
			out, err := c.AgentProtocol(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(out, func(w io.Writer) {
				if action == "show" {
					fmt.Fprintln(w, out.Content)
				} else {
					fmt.Fprintf(w, "Acknowledged protocol %s\n", out.Version)
				}
			})
			return nil
		}}
		sub.Flags().StringVar(&runID, "run-id", os.Getenv("TEAM_WORKER_RUN_ID"), "Current run ID; show without one only inspects context")
		sub.Flags().StringVar(&version, "version", "", "Exact protocol revision to acknowledge")
		cmd.AddCommand(sub)
	}
	return cmd
}
