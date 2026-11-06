package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (c *Client) CreateDecisionRequest(ctx context.Context, team string, in store.CreateDecisionInput) (model.DecisionRequest, error) {
	var out model.DecisionRequest
	return out, c.do(ctx, http.MethodPost, teamPath(team, "requests"), nil, in, &out)
}

func (c *Client) ListDecisionRequests(ctx context.Context, team string, status model.DecisionStatus) ([]model.DecisionRequest, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", string(status))
	}
	var out []model.DecisionRequest
	return out, c.do(ctx, http.MethodGet, teamPath(team, "requests"), q, nil, &out)
}

func (c *Client) GetDecisionRequest(ctx context.Context, team, id string) (model.DecisionRequest, error) {
	var out model.DecisionRequest
	return out, c.do(ctx, http.MethodGet, teamPath(team, "requests", id), nil, nil, &out)
}

// DecideRequest decides a request; an empty DecidedBy decides as the operator.
func (c *Client) DecideRequest(ctx context.Context, team, id string, in store.DecideInput) (model.DecisionRequest, error) {
	var out model.DecisionRequest
	return out, c.do(ctx, http.MethodPost, teamPath(team, "requests", id, "decide"), nil, in, &out)
}

func (c *Client) AutoReview(ctx context.Context, team string) (model.AutoReviewStatus, error) {
	var out model.AutoReviewStatus
	return out, c.do(ctx, http.MethodGet, teamPath(team, "auto-review"), nil, nil, &out)
}

func (c *Client) SetAutoReview(ctx context.Context, team string, in model.AutoReview) (model.AutoReviewStatus, error) {
	var out model.AutoReviewStatus
	return out, c.do(ctx, http.MethodPut, teamPath(team, "auto-review"), nil, in, &out)
}
