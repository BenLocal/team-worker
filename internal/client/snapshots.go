package client

import (
	"context"
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
)

// CreateSnapshot saves the team's current state under an optional tag.
func (c *Client) CreateSnapshot(ctx context.Context, team, tag string) (model.SnapshotMeta, error) {
	var out model.SnapshotMeta
	return out, c.do(ctx, http.MethodPost, teamPath(team, "snapshots"), nil, map[string]string{"tag": tag}, &out)
}

// ListSnapshots returns the team's snapshots, newest first.
func (c *Client) ListSnapshots(ctx context.Context, team string) ([]model.SnapshotMeta, error) {
	var out []model.SnapshotMeta
	return out, c.do(ctx, http.MethodGet, teamPath(team, "snapshots"), nil, nil, &out)
}

// RestoreSnapshot replaces the team's state with a snapshot; with dryRun it
// only reports what it would replace.
func (c *Client) RestoreSnapshot(ctx context.Context, team, id string, dryRun bool) (model.RestoreSummary, error) {
	var out model.RestoreSummary
	return out, c.do(ctx, http.MethodPost, teamPath(team, "snapshots", id, "restore"), nil,
		map[string]bool{"dryRun": dryRun}, &out)
}

// DeleteSnapshot removes a snapshot.
func (c *Client) DeleteSnapshot(ctx context.Context, team, id string) error {
	return c.do(ctx, http.MethodDelete, teamPath(team, "snapshots", id), nil, nil, nil)
}
