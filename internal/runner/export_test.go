package runner

import (
	"context"

	"github.com/benlocal/team-worker/internal/client"
)

// PrepareWorkspaces plans and creates an agent's worktrees as spawn does,
// returning the undo spawn runs when the agent then fails to start.
func PrepareWorkspaces(ctx context.Context, c *client.Client, dataDir string, req Request) (string, func(), error) {
	planned, cwd, err := planWorkspaces(ctx, c, dataDir, req, nil)
	if err != nil {
		return "", nil, err
	}
	undo, err := createWorkspaces(ctx, c, planned)
	return cwd, undo, err
}
