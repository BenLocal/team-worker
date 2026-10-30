package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (c *Client) RequestJoin(ctx context.Context, team string, in store.JoinRequestInput) (model.JoinRequest, error) {
	var out model.JoinRequest
	return out, c.do(ctx, http.MethodPost, teamPath(team, "join-requests"), nil, in, &out)
}

func (c *Client) GetJoinRequest(ctx context.Context, team, id string) (model.JoinRequest, error) {
	var out model.JoinRequest
	return out, c.do(ctx, http.MethodGet, teamPath(team, "join-requests", id), nil, nil, &out)
}

func (c *Client) ListJoinRequests(ctx context.Context, team string, status model.JoinStatus) ([]model.JoinRequest, error) {
	var out []model.JoinRequest
	var q url.Values
	if status != "" {
		q = url.Values{"status": {string(status)}}
	}
	return out, c.do(ctx, http.MethodGet, teamPath(team, "join-requests"), q, nil, &out)
}

func (c *Client) ApproveJoin(ctx context.Context, team, id string, in store.JoinDecisionInput) (model.JoinRequest, error) {
	var out model.JoinRequest
	return out, c.do(ctx, http.MethodPost, teamPath(team, "join-requests", id, "approve"), nil, in, &out)
}

func (c *Client) RejectJoin(ctx context.Context, team, id string, in store.JoinDecisionInput) (model.JoinRequest, error) {
	var out model.JoinRequest
	return out, c.do(ctx, http.MethodPost, teamPath(team, "join-requests", id, "reject"), nil, in, &out)
}
