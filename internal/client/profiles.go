package client

import (
	"context"
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/profile"
)

// Profiles returns the runtime profiles and endpoint presets in the
// daemon's config.json, which spawn and launch apply.
func (c *Client) Profiles(ctx context.Context) (profile.Config, error) {
	var out profile.Config
	return out, c.do(ctx, http.MethodGet, "/v1/profiles", nil, nil, &out)
}

// SaveProfiles replaces the daemon's profiles and presets.
func (c *Client) SaveProfiles(ctx context.Context, cfg profile.Config) error {
	return c.do(ctx, http.MethodPut, "/v1/profiles", nil, cfg, nil)
}

// TestProfile has the daemon run a profile once with prompt in cwd.
func (c *Client) TestProfile(ctx context.Context, name, prompt, cwd string) (model.ProfileTest, error) {
	var out model.ProfileTest
	body := map[string]string{"prompt": prompt, "cwd": cwd}
	return out, c.do(ctx, http.MethodPost, "/v1/profiles/"+name+"/test", nil, body, &out)
}
