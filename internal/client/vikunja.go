package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
)

func (c *Client) VikunjaSettings(ctx context.Context) ([]model.VikunjaHookSettings, error) {
	var out []model.VikunjaHookSettings
	return out, c.do(ctx, http.MethodGet, "/v1/hooks/vikunja/settings", nil, nil, &out)
}

func (c *Client) SetVikunjaSettings(ctx context.Context, team string, input model.VikunjaHookUpdate) (model.VikunjaHookSettings, error) {
	var out model.VikunjaHookSettings
	return out, c.do(ctx, http.MethodPut, "/v1/hooks/vikunja/settings/"+url.PathEscape(team), nil, input, &out)
}
