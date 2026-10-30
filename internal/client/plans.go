package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (c *Client) SubmitPlan(ctx context.Context, team string, in store.SubmitPlanInput) (model.Plan, error) {
	var out model.Plan
	return out, c.do(ctx, http.MethodPost, teamPath(team, "plans"), nil, in, &out)
}

// DecidePlan approves the plan when approve is true and rejects it otherwise.
func (c *Client) DecidePlan(ctx context.Context, team, id string, approve bool, in store.DecidePlanInput) (model.Plan, error) {
	action := "reject"
	if approve {
		action = "approve"
	}
	var out model.Plan
	return out, c.do(ctx, http.MethodPost, teamPath(team, "plans", id, action), nil, in, &out)
}

func (c *Client) GetPlan(ctx context.Context, team, id string) (model.Plan, error) {
	var out model.Plan
	return out, c.do(ctx, http.MethodGet, teamPath(team, "plans", id), nil, nil, &out)
}

func (c *Client) ListPlans(ctx context.Context, team, agent string, status model.PlanStatus) ([]model.Plan, error) {
	q := url.Values{}
	if agent != "" {
		q.Set("agent", agent)
	}
	if status != "" {
		q.Set("status", string(status))
	}
	var out []model.Plan
	return out, c.do(ctx, http.MethodGet, teamPath(team, "plans"), q, nil, &out)
}

func (c *Client) SetPlanRequired(ctx context.Context, team, name, user string, required bool) error {
	body := map[string]any{"user": user, "required": required}
	return c.do(ctx, http.MethodPut, teamPath(team, "members", name, "plan-required"), nil, body, nil)
}
