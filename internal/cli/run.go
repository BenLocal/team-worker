package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// runCmd is ClawTeam's `run`: start one agent CLI with team-worker's
// identity, prompt and lifecycle, creating its team when needed. It is a
// short form of spawn; --resume continues the team leader's saved session.
func (a *app) runCmd() *cobra.Command {
	var team, profileName, backend string
	var resume, keepAlive bool
	var repos []string
	cmd := &cobra.Command{
		Use:   "run CLI [GOAL]",
		Short: "Start an agent CLI under team-worker's lifecycle, creating its team when needed",
		Long: "Starts CLI (claude, codex, gemini, …) as a member of --team, with GOAL as its\n" +
			"instructions. A missing team is created with the agent as its leader. With --resume\n" +
			"and an existing team, the leader's saved session is resumed instead, e.g.\n" +
			"  team-worker run claude \"Fix the login bug\"\n" +
			"  team-worker run claude --resume",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if backend != "tmux" && backend != "subprocess" {
				return a.fail(fmt.Errorf("unknown backend '%s': use tmux or subprocess", backend))
			}
			cli, goal := args[0], ""
			if len(args) == 2 {
				goal = args[1]
			}
			c := a.client()
			existing, err := c.GetTeam(cmd.Context(), team)
			if err != nil && !client.IsNotFound(err) {
				return a.fail(err)
			}
			if resume && err == nil {
				leader := leaderOf(existing)
				if leader == "" {
					return a.fail(fmt.Errorf("Team '%s' has no member to resume", team))
				}
				req := model.ResumeRequest{Team: team, Name: leader, User: config.IdentityFromEnv().User,
					KeepAlive: &keepAlive, Profile: profileName}
				if goal != "" {
					req.Task = &goal
				}
				res, err := c.ResumeAgent(cmd.Context(), req)
				if err != nil {
					return a.fail(err)
				}
				a.emit(res, func(w io.Writer) { printSpawned(w, team, res) })
				return nil
			}
			req := model.SpawnRequest{
				Backend:   backend,
				Team:      team,
				Name:      cli + "-" + store.NewAgentID()[:6],
				AgentType: cli,
				Command:   []string{cli},
				Task:      goal,
				TaskSet:   goal != "",
				Profile:   profileName,
				User:      config.IdentityFromEnv().User,
				SkipPerms: true,
				KeepAlive: keepAlive,
				Repos:     repos,
				// spawn's --max-restarts default.
				MaxRestarts: 3,
			}
			req.Workspace, req.CallerDir = workspaceFlags(cmd, c.Remote())
			res, err := c.SpawnAgent(cmd.Context(), req)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) { printSpawned(w, team, res) })
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&team, "team", "t", "default", "Team name")
	f.StringVar(&backend, "backend", "tmux", "Backend: tmux or subprocess")
	f.StringVarP(&profileName, "profile", "P", "", "Runtime profile")
	addWorkspaceFlags(cmd, &repos)
	f.BoolVar(&resume, "resume", false, "Resume the team leader's saved session when the team exists")
	f.BoolVar(&keepAlive, "keepalive", false, "Resume the exact session after unapproved exits")
	return cmd
}
