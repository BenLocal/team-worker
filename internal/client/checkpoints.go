package client

import (
	"context"
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (c *Client) CreateCheckpoint(ctx context.Context, team string, in store.CreateCheckpointInput) (model.Checkpoint, error) {
	var out model.Checkpoint
	return out, c.do(ctx, http.MethodPost, teamPath(team, "checkpoints"), nil, in, &out)
}
func (c *Client) ListCheckpoints(ctx context.Context, team string) ([]model.Checkpoint, error) {
	var out []model.Checkpoint
	return out, c.do(ctx, http.MethodGet, teamPath(team, "checkpoints"), nil, nil, &out)
}
func (c *Client) GetCheckpoint(ctx context.Context, team, id string) (model.Checkpoint, error) {
	var out model.Checkpoint
	return out, c.do(ctx, http.MethodGet, teamPath(team, "checkpoints", id), nil, nil, &out)
}
func (c *Client) UpdateCheckpoint(ctx context.Context, team, id string, in store.CheckpointUpdateInput) (model.Checkpoint, error) {
	var out model.Checkpoint
	return out, c.do(ctx, http.MethodPost, teamPath(team, "checkpoints", id), nil, in, &out)
}
