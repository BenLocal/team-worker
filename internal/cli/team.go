package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) teamCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "team", Short: "Team management"}
	cmd.AddCommand(a.teamSpawnCmd(), a.teamAddMemberCmd(), a.teamDiscoverCmd(), a.teamStatusCmd(),
		a.teamCleanupCmd(), a.teamPromptCmd())
	cmd.AddCommand(a.joinCmds()...)
	return cmd
}

func (a *app) teamPromptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Shared instructions every spawned agent receives",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "show TEAM",
		Short: "Print the team instructions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt, err := a.client().TeamPrompt(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"team": args[0], "prompt": prompt}, func(w io.Writer) {
				if prompt == "" {
					fmt.Fprintln(w, "No team instructions set")
					return
				}
				fmt.Fprintln(w, prompt)
			})
			return nil
		},
	})
	var file string
	set := &cobra.Command{
		Use:   "set TEAM [TEXT]",
		Short: "Replace the team instructions (from TEXT, --file or stdin; empty clears them)",
		Long: "Replace the team instructions. New agents get them in their first prompt; running\n" +
			"members receive an inbox notice to re-read them.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var text string
			switch {
			case len(args) == 2:
				text = args[1]
			case file != "":
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
			text = strings.TrimRight(text, "\n")
			changed, err := a.client().SetTeamPrompt(cmd.Context(), args[0], text)
			if err != nil {
				return a.fail(err)
			}
			if changed {
				_, err := a.client().Broadcast(cmd.Context(), args[0], store.BroadcastInput{
					From: config.IdentityFromEnv().AgentName, Type: "message",
					Content: fmt.Sprintf("Team instructions were updated. Read them with "+
						"`team-worker team prompt show %s` and follow them from now on.", args[0]),
				})
				if err != nil {
					return a.fail(err)
				}
			}
			a.emit(map[string]any{"team": args[0], "changed": changed}, func(w io.Writer) {
				if changed {
					fmt.Fprintln(w, "OK Team instructions saved; members were notified")
				} else {
					fmt.Fprintln(w, "Team instructions unchanged")
				}
			})
			return nil
		},
	}
	set.Flags().StringVarP(&file, "file", "f", "", "Read the instructions from a file")
	cmd.AddCommand(set)
	return cmd
}

func (a *app) teamAddMemberCmd() *cobra.Command {
	var agentType, agentID string
	var planRequired bool
	cmd := &cobra.Command{
		Use:   "add-member TEAM NAME",
		Short: "Add a member to a team",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			member, err := a.client().AddMember(cmd.Context(), args[0], store.AddMemberInput{
				Name:         args[1],
				AgentID:      agentID,
				AgentType:    agentType,
				User:         config.IdentityFromEnv().User,
				PlanRequired: planRequired,
			})
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"status": "added", "team": args[0], "member": member}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Added member '%s' to team '%s' (id: %s)\n", member.Name, args[0], member.AgentID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&agentType, "agent-type", "general-purpose", "Agent type")
	cmd.Flags().StringVar(&agentID, "agent-id", "", "Agent ID (default: auto-generated)")
	cmd.Flags().BoolVar(&planRequired, "plan-required", false, "Require an approved plan before the member starts or completes tasks")
	return cmd
}

func (a *app) teamSpawnCmd() *cobra.Command {
	var description, agentName, agentType string
	cmd := &cobra.Command{
		Use:   "spawn-team NAME",
		Short: "Create a new team and register the leader",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := config.IdentityFromEnv()
			leaderID := id.AgentID
			if leaderID == "" {
				leaderID = store.NewAgentID()
			}
			team, err := a.client().CreateTeam(cmd.Context(), store.CreateTeamInput{
				Name:            args[0],
				Description:     description,
				LeaderName:      agentName,
				LeaderID:        leaderID,
				User:            id.User,
				LeaderAgentType: agentType,
			})
			if err != nil {
				return a.fail(err)
			}
			result := map[string]string{
				"status":      "created",
				"team":        team.Name,
				"leadAgentId": team.LeadAgentID,
				"leaderName":  agentName,
			}
			if id.User != "" {
				result["user"] = id.User
			}
			a.emit(result, func(w io.Writer) {
				fmt.Fprintf(w, "OK Team '%s' created\n", team.Name)
				fmt.Fprintf(w, "  Leader: %s (id: %s)\n", agentName, team.LeadAgentID)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&description, "description", "d", "", "Team description")
	cmd.Flags().StringVarP(&agentName, "agent-name", "n", "leader", "Leader agent name")
	cmd.Flags().StringVar(&agentType, "agent-type", "leader", "Leader agent type")
	return cmd
}

func (a *app) teamDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List existing teams",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			teams, err := a.client().ListTeams(cmd.Context())
			if err != nil {
				return a.fail(err)
			}
			a.emit(teams, func(w io.Writer) {
				if len(teams) == 0 {
					fmt.Fprintln(w, "No teams found")
					return
				}
				tw := newTable(w, "NAME", "DESCRIPTION", "LEAD", "MEMBERS")
				for _, t := range teams {
					row(tw, t.Name, t.Description, t.LeadAgentID, fmt.Sprint(t.MemberCount))
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func (a *app) teamStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status TEAM",
		Short: "Show team status and members",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			team, err := a.client().GetTeam(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(team, func(w io.Writer) { printTeam(w, team) })
			return nil
		},
	}
}

func printTeam(w io.Writer, t model.Team) {
	fmt.Fprintf(w, "Team: %s\n", t.Name)
	if t.Description != "" {
		fmt.Fprintf(w, "  %s\n", t.Description)
	}
	fmt.Fprintf(w, "  Created: %s\n\n", formatTimestamp(t.CreatedAt))
	hasUser := false
	for _, m := range t.Members {
		hasUser = hasUser || m.User != ""
	}
	headers := []string{"NAME"}
	if hasUser {
		headers = append(headers, "USER")
	}
	tw := newTable(w, append(headers, "ID", "TYPE", "JOINED")...)
	for _, m := range t.Members {
		cols := []string{m.Name}
		if hasUser {
			cols = append(cols, m.User)
		}
		row(tw, append(cols, m.AgentID, m.AgentType, formatTimestamp(m.JoinedAt))...)
	}
	tw.Flush()
}

func (a *app) teamCleanupCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "cleanup TEAM",
		Short: "Delete a team and all its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !force && !a.jsonOut && !a.confirm(fmt.Sprintf("Delete team '%s' and all its data?", name)) {
				return a.fail(errors.New("aborted"))
			}
			agents, err := a.client().StopTeamAgents(cmd.Context(), name)
			if err != nil && !client.IsNotFound(err) {
				return a.fail(err)
			}
			for _, ag := range agents {
				if ag.Status != model.AgentRunning && ag.Backend != "tmux" {
					continue
				}
				if err := proc.Stop(ag); err != nil {
					return a.fail(err)
				}
			}
			deleted, err := a.client().DeleteTeam(cmd.Context(), name)
			if err != nil {
				return a.fail(err)
			}
			status := "cleaned"
			if !deleted {
				status = "not_found"
			}
			a.emit(map[string]string{"status": status, "team": name}, func(w io.Writer) {
				if deleted {
					fmt.Fprintf(w, "OK Team '%s' deleted\n", name)
				} else {
					fmt.Fprintf(w, "Team '%s' not found\n", name)
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

func (a *app) identityCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "identity", Short: "Agent identity"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the identity taken from TEAM_WORKER_* variables",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id := config.IdentityFromEnv()
			a.emit(id, func(w io.Writer) {
				fmt.Fprintf(w, "Agent:  %s\n", id.AgentName)
				fmt.Fprintf(w, "ID:     %s\n", id.AgentID)
				fmt.Fprintf(w, "Type:   %s\n", id.AgentType)
				fmt.Fprintf(w, "Team:   %s\n", id.TeamName)
				fmt.Fprintf(w, "User:   %s\n", id.User)
				fmt.Fprintf(w, "Leader: %t\n", id.IsLeader)
				fmt.Fprintf(w, "Plan approval required: %t\n", id.PlanModeRequired)
			})
			return nil
		},
	})
	return cmd
}
