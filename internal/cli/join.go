package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// joinPollInterval is how often request-join --wait checks the request; tests shorten it.
var joinPollInterval = time.Second

func (a *app) joinCmds() []*cobra.Command {
	return []*cobra.Command{a.requestJoinCmd(), a.joinStatusCmd(), a.approveJoinCmd(), a.rejectJoinCmd(), a.joinRequestsCmd()}
}

// joinResult is the ClawTeam-compatible JSON for a request in its current state.
func joinResult(r model.JoinRequest) map[string]any {
	switch r.Status {
	case model.JoinApproved:
		return map[string]any{"status": "approved", "requestId": r.RequestID, "assignedName": r.AssignedName,
			"agentId": r.AgentID, "teamName": r.TeamName}
	case model.JoinRejected:
		return map[string]any{"status": "rejected", "requestId": r.RequestID, "reason": r.Reason}
	}
	return map[string]any{"status": "pending", "requestId": r.RequestID, "teamName": r.TeamName, "proposedName": r.ProposedName}
}

func printJoin(w io.Writer, r model.JoinRequest) {
	switch r.Status {
	case model.JoinApproved:
		fmt.Fprintf(w, "OK Joined team '%s' as '%s' (id: %s)\n", r.TeamName, r.AssignedName, r.AgentID)
		fmt.Fprintf(w, "  Set TEAM_WORKER_AGENT_NAME=%s and TEAM_WORKER_AGENT_ID=%s to act as this member.\n", r.AssignedName, r.AgentID)
	case model.JoinRejected:
		fmt.Fprintf(w, "Join request %s was rejected", r.RequestID)
		if r.Reason != "" {
			fmt.Fprintf(w, ": %s", r.Reason)
		}
		fmt.Fprintln(w)
	default:
		fmt.Fprintf(w, "Join request %s for '%s' is pending in team '%s'\n", r.RequestID, r.ProposedName, r.TeamName)
	}
}

func (a *app) requestJoinCmd() *cobra.Command {
	var capabilities string
	var timeout int
	var wait, noWait bool
	cmd := &cobra.Command{
		Use:   "request-join TEAM PROPOSED_NAME",
		Short: "Ask the team leader to let you join, and wait for the answer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			r, err := c.RequestJoin(cmd.Context(), args[0], store.JoinRequestInput{
				ProposedName: args[1], User: config.IdentityFromEnv().User, Capabilities: capabilities,
			})
			if err != nil {
				return a.fail(err)
			}
			if wait && !noWait {
				deadline := time.Now().Add(time.Duration(timeout) * time.Second)
				if !a.jsonOut {
					fmt.Fprintf(a.errOut, "Waiting up to %ds for the leader to answer join request %s...\n", timeout, r.RequestID)
				}
				for r.Status == model.JoinPending && time.Now().Before(deadline) {
					select {
					case <-cmd.Context().Done():
						return a.fail(cmd.Context().Err())
					case <-time.After(min(joinPollInterval, time.Until(deadline))):
					}
					if r, err = c.GetJoinRequest(cmd.Context(), args[0], r.RequestID); err != nil {
						return a.fail(err)
					}
				}
			}
			a.emit(joinResult(r), func(w io.Writer) { printJoin(w, r) })
			if r.Status == model.JoinRejected {
				return exitError{}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&capabilities, "capabilities", "c", "", "What you can do, shown to the leader")
	cmd.Flags().IntVarP(&timeout, "timeout", "t", 300, "Seconds to wait for an answer")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the leader's answer")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Return as soon as the request is recorded")
	return cmd
}

func (a *app) joinStatusCmd() *cobra.Command {
	var proposedName string
	cmd := &cobra.Command{
		Use:   "join-status TEAM REQUEST_ID",
		Short: "Show whether a join request was approved, rejected or is still pending",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.client().GetJoinRequest(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			a.emit(joinResult(r), func(w io.Writer) { printJoin(w, r) })
			return nil
		},
	}
	// Accepted for ClawTeam compatibility; the stored request already names the requester.
	cmd.Flags().StringVar(&proposedName, "proposed-name", "", "Proposed name of the request (not needed)")
	return cmd
}

func joinDecision() store.JoinDecisionInput {
	id := config.IdentityFromEnv()
	return store.JoinDecisionInput{Caller: id.AgentName, CallerUser: id.User}
}

func (a *app) approveJoinCmd() *cobra.Command {
	var assignedName string
	cmd := &cobra.Command{
		Use:   "approve-join TEAM REQUEST_ID",
		Short: "Approve a join request and add the requester as a member (leader only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := joinDecision()
			in.AssignedName = assignedName
			r, err := a.client().ApproveJoin(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(joinResult(r), func(w io.Writer) {
				fmt.Fprintf(w, "OK Approved %s: '%s' joined team '%s' (id: %s)\n", r.RequestID, r.AssignedName, r.TeamName, r.AgentID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&assignedName, "assigned-name", "", "Member name to give (default: the proposed name)")
	return cmd
}

func (a *app) rejectJoinCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "reject-join TEAM REQUEST_ID",
		Short: "Reject a join request (leader only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := joinDecision()
			in.Reason = reason
			r, err := a.client().RejectJoin(cmd.Context(), args[0], args[1], in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(joinResult(r), func(w io.Writer) { fmt.Fprintf(w, "OK Rejected join request %s\n", r.RequestID) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Reason given to the requester")
	return cmd
}

func (a *app) joinRequestsCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "join-requests TEAM",
		Short: "List a team's join requests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := a.client().ListJoinRequests(cmd.Context(), args[0], model.JoinStatus(status))
			if err != nil {
				return a.fail(err)
			}
			a.emit(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No join requests")
					return
				}
				tw := newTable(w, "REQUEST", "NAME", "STATUS", "CAPABILITIES", "CREATED")
				for _, r := range list {
					row(tw, r.RequestID, r.ProposedName, string(r.Status), truncate(r.Capabilities, 40), formatTimestamp(r.CreatedAt))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Only requests in this state: pending, approved or rejected")
	return cmd
}
