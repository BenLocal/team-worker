package client

import (
	"context"
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// harnessRun names a run in a path; an empty id means the team's current run.
func harnessRun(id string) string {
	if id == "" {
		return "current"
	}
	return id
}

func (c *Client) StartHarness(ctx context.Context, team string, in store.StartHarnessInput) (model.HarnessRun, error) {
	var out model.HarnessRun
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness"), nil, in, &out)
}

func (c *Client) ListHarness(ctx context.Context, team string) ([]model.HarnessRun, error) {
	var out []model.HarnessRun
	return out, c.do(ctx, http.MethodGet, teamPath(team, "harness"), nil, nil, &out)
}

func (c *Client) HarnessStatus(ctx context.Context, team, id string) (model.HarnessStatus, error) {
	var out model.HarnessStatus
	return out, c.do(ctx, http.MethodGet, teamPath(team, "harness", harnessRun(id)), nil, nil, &out)
}

func (c *Client) AdvanceHarness(ctx context.Context, team, id string) (model.HarnessAdvance, error) {
	var out model.HarnessAdvance
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness", harnessRun(id), "advance"), nil, struct{}{}, &out)
}

// ApproveHarness approves the newest pending plan of the run's planners.
func (c *Client) ApproveHarness(ctx context.Context, team, id, caller, feedback string) (model.Plan, error) {
	var out model.Plan
	body := map[string]string{"caller": caller, "feedback": feedback}
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness", harnessRun(id), "approve"), nil, body, &out)
}

func (c *Client) AbortHarness(ctx context.Context, team, id, reason string) (model.HarnessStatus, error) {
	var out model.HarnessStatus
	body := map[string]string{"reason": reason}
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness", harnessRun(id), "abort"), nil, body, &out)
}

func (c *Client) PutArtifact(ctx context.Context, team, id, name, content string) (model.HarnessArtifact, error) {
	var out model.HarnessArtifact
	body := map[string]string{"content": content}
	return out, c.do(ctx, http.MethodPut, teamPath(team, "harness", harnessRun(id), "artifacts", name), nil, body, &out)
}

func (c *Client) GetArtifact(ctx context.Context, team, id, name string) (model.HarnessArtifact, error) {
	var out model.HarnessArtifact
	return out, c.do(ctx, http.MethodGet, teamPath(team, "harness", harnessRun(id), "artifacts", name), nil, nil, &out)
}

func (c *Client) ListArtifacts(ctx context.Context, team, id string) ([]model.HarnessArtifact, error) {
	var out []model.HarnessArtifact
	return out, c.do(ctx, http.MethodGet, teamPath(team, "harness", harnessRun(id), "artifacts"), nil, nil, &out)
}

func (c *Client) CreateHarnessTask(ctx context.Context, team, id string, in store.CreateTaskInput) (model.Task, error) {
	var out model.Task
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness", harnessRun(id), "tasks"), nil, in, &out)
}

func (c *Client) AddHarnessAgent(ctx context.Context, team, id string, in store.HarnessAgentInput) (model.HarnessAgent, error) {
	var out model.HarnessAgent
	return out, c.do(ctx, http.MethodPost, teamPath(team, "harness", harnessRun(id), "agents"), nil, in, &out)
}
