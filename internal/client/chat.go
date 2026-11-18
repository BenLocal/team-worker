package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func (c *Client) SubmitChatResult(ctx context.Context, team, id string, result model.ChatResultInput) error {
	return c.do(ctx, http.MethodPost, "/v1/teams/"+url.PathEscape(team)+"/chat/requests/"+url.PathEscape(id)+"/results", nil, result, nil)
}

func (c *Client) BlockedChatRequests(ctx context.Context) ([]model.ChatBlockedItem, error) {
	var out []model.ChatBlockedItem
	return out, c.do(ctx, http.MethodGet, "/v1/chat/blocked", nil, nil, &out)
}

func (c *Client) FeishuApp(ctx context.Context) (model.FeishuAppInfo, error) {
	var out model.FeishuAppInfo
	return out, c.do(ctx, http.MethodGet, "/v1/chat/feishu/app", nil, nil, &out)
}

func (c *Client) SetFeishuApp(ctx context.Context, app config.FeishuApp) (model.FeishuAppInfo, error) {
	var out model.FeishuAppInfo
	return out, c.do(ctx, http.MethodPut, "/v1/chat/feishu/app", nil, app, &out)
}

func (c *Client) ChatSettings(ctx context.Context) (model.ChatSettings, error) {
	var out model.ChatSettings
	return out, c.do(ctx, http.MethodGet, "/v1/chat/settings", nil, nil, &out)
}

func (c *Client) SetChatSettings(ctx context.Context, settings model.ChatSettings) (model.ChatSettings, error) {
	var out model.ChatSettings
	return out, c.do(ctx, http.MethodPut, "/v1/chat/settings", nil, settings, &out)
}

func (c *Client) ChatConnections(ctx context.Context) ([]model.ChatConnection, error) {
	var out []model.ChatConnection
	return out, c.do(ctx, http.MethodGet, "/v1/chat/connections", nil, nil, &out)
}
