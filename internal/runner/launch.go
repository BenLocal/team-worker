package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/templates"
)

type (
	LaunchRequest = model.LaunchRequest
	LaunchedAgent = model.LaunchedAgent
	LaunchResult  = model.LaunchResult
)

// Launch registers the leader and members, creates the template's tasks with
// their dependencies, then spawns the leader and every worker. Every agent's
// command and environment are checked first, so a typo does not leave a
// half-launched team behind.
func Launch(ctx context.Context, e Env, in LaunchRequest) (LaunchResult, error) {
	var tmpl templates.Template
	var err error
	if in.TemplateText != "" {
		name := strings.TrimSuffix(in.Template, ".toml")
		if i := strings.LastIndexAny(name, `/\`); i >= 0 {
			name = name[i+1:]
		}
		tmpl, err = templates.Parse([]byte(in.TemplateText), name, in.Template)
	} else {
		tmpl, err = templates.Load(in.Template, config.TemplateDir())
	}
	if err != nil {
		return LaunchResult{}, err
	}
	teamName, backend := in.Team, in.Backend
	if teamName == "" {
		teamName = tmpl.Name + "-" + store.NewAgentID()[:6]
	}
	if backend == "" {
		backend = tmpl.Backend
	}
	c := e.Client
	leader := tmpl.Leader

	var reqs []Request
	for _, ag := range append([]templates.Agent{leader}, tmpl.Agents...) {
		command := ag.Command
		if len(in.Command) > 0 {
			command = in.Command
		} else if len(command) == 0 {
			command = tmpl.Command
		}
		var layers []spawn.EnvLayer
		if len(tmpl.Env) > 0 {
			layers = append(layers, spawn.EnvLayer{Name: "template " + tmpl.Name, Set: tmpl.Env})
		}
		if len(ag.Env) > 0 {
			layers = append(layers, spawn.EnvLayer{Name: "template agent " + ag.Name, Set: ag.Env})
		}
		req := Request{
			Team: teamName, Name: ag.Name, AgentType: agentType(ag), Backend: backend, Command: command,
			Task:    templates.Render(ag.Task, in.Goal, teamName, ag.Name, leader.Name),
			TaskSet: true, User: in.User,
			Cwd: in.Cwd, EnvLayers: append(layers, in.EnvLayers...), SkipPerms: !in.NoSkip,
		}
		if in.Workspace || len(in.Repos) > 0 {
			on := true
			req.Workspace, req.Repos, req.CallerDir = &on, in.Repos, in.CallerDir
		}
		// The profile layer goes under the template's own environment.
		if err := ApplyProfile(&req, in.Profile); err != nil {
			return LaunchResult{}, fmt.Errorf("agent '%s': %w", ag.Name, err)
		}
		reqs = append(reqs, req)
	}
	for _, req := range reqs {
		req.DryRun = true
		if _, err := Spawn(ctx, e, req); err != nil {
			return LaunchResult{}, fmt.Errorf("agent '%s': %w", req.Name, err)
		}
	}

	if _, err := c.CreateTeam(ctx, store.CreateTeamInput{Name: teamName, Description: tmpl.Description,
		LeaderName: leader.Name, User: in.User, LeaderAgentType: agentType(leader)}); err != nil {
		return LaunchResult{}, err
	}
	if in.TeamPrompt != "" {
		if _, err := c.SetTeamPrompt(ctx, teamName, in.TeamPrompt); err != nil {
			return LaunchResult{}, err
		}
	}
	for _, ag := range tmpl.Agents {
		if _, err := c.AddMember(ctx, teamName, store.AddMemberInput{
			Name: ag.Name, AgentType: agentType(ag), User: in.User}); err != nil {
			return LaunchResult{}, err
		}
	}
	ids := map[string]string{}
	for _, task := range tmpl.Tasks {
		var deps []string
		for _, key := range task.BlockedBy {
			deps = append(deps, ids[key])
		}
		created, err := c.CreateTask(ctx, teamName, store.CreateTaskInput{Subject: task.Subject,
			Description: templates.Render(task.Description, in.Goal, teamName, task.Owner, leader.Name),
			Owner:       task.Owner, Priority: model.Priority(task.Priority), BlockedBy: deps})
		if err != nil {
			return LaunchResult{}, err
		}
		if task.Key != "" {
			ids[task.Key] = created.ID
		}
	}

	out := LaunchResult{Status: "launched", Team: teamName, Template: tmpl.Name, Backend: backend}
	for _, req := range reqs {
		res, err := Spawn(ctx, e, req)
		entry := LaunchedAgent{SpawnResult: res}
		if err != nil {
			out.Status = "partial"
			entry.AgentName, entry.Error = req.Name, err.Error()
		}
		out.Agents = append(out.Agents, entry)
	}
	return out, nil
}

func agentType(a templates.Agent) string {
	if a.Type == "" {
		return "general-purpose"
	}
	return a.Type
}
