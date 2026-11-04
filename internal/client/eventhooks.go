package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// ListEventHooks returns every user event hook in the order they run.
func (c *Client) ListEventHooks(ctx context.Context) ([]model.EventHook, error) {
	var out []model.EventHook
	return out, c.do(ctx, http.MethodGet, "/v1/event-hooks", nil, nil, &out)
}

// AddEventHook adds a user event hook.
func (c *Client) AddEventHook(ctx context.Context, in store.EventHookInput) (model.EventHook, error) {
	var out model.EventHook
	return out, c.do(ctx, http.MethodPost, "/v1/event-hooks", nil, in, &out)
}

// RemoveEventHooks removes an event type's hooks, only those with the given
// command when it is not empty, and returns how many it removed.
func (c *Client) RemoveEventHooks(ctx context.Context, event, command string) (int, error) {
	q := url.Values{"event": {event}}
	if command != "" {
		q.Set("command", command)
	}
	var out struct {
		Removed int `json:"removed"`
	}
	err := c.do(ctx, http.MethodDelete, "/v1/event-hooks", q, nil, &out)
	return out.Removed, err
}

// TestEventHook runs an event type's hooks on the daemon's machine with a
// made-up event and returns their results.
func (c *Client) TestEventHook(ctx context.Context, event, team, agent string) (model.HookTestResult, error) {
	var out model.HookTestResult
	return out, c.do(ctx, http.MethodPost, "/v1/event-hooks/test", nil,
		map[string]string{"event": event, "team": team, "agent": agent}, &out)
}
