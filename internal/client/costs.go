package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
)

func (c *Client) ReportCost(ctx context.Context, team string, e model.CostEvent) (model.CostEvent, error) {
	var out model.CostEvent
	return out, c.do(ctx, http.MethodPost, teamPath(team, "costs"), nil, e, &out)
}

func (c *Client) RecordSessionUsage(ctx context.Context, team string, e model.CostEvent) (model.CostEvent, error) {
	var out model.CostEvent
	return out, c.do(ctx, http.MethodPut, teamPath(team, "costs", "sessions"), nil, e, &out)
}

func (c *Client) Costs(ctx context.Context, team, agent string) (model.CostView, error) {
	var out model.CostView
	var q url.Values
	if agent != "" {
		q = url.Values{"agent": {agent}}
	}
	return out, c.do(ctx, http.MethodGet, teamPath(team, "costs"), q, nil, &out)
}

func (c *Client) Budget(ctx context.Context, team string) (model.Budget, error) {
	var out model.Budget
	return out, c.do(ctx, http.MethodGet, teamPath(team, "budget"), nil, nil, &out)
}

func (c *Client) SetBudget(ctx context.Context, team string, cents float64, enforce bool) (model.Budget, error) {
	var out model.Budget
	body := map[string]any{"budgetCents": cents, "enforce": enforce}
	return out, c.do(ctx, http.MethodPut, teamPath(team, "budget"), nil, body, &out)
}
