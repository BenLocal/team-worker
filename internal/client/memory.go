package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
)

// Memory performs a file-backed operation on a stable member identity.
func (c *Client) Memory(ctx context.Context, id model.MemoryIdentity, action string, in model.MemoryRequest) (model.MemoryResult, error) {
	var out model.MemoryResult
	err := c.do(ctx, http.MethodPost, teamPath(id.Team, "members", id.Name, "memory", action), url.Values{"user": {id.User}}, in, &out)
	return out, err
}
