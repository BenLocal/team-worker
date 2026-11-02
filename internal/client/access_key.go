package client

import (
	"context"
	"errors"
)

// AccessKey retrieves the shared key through the owner-only Unix socket.
func (c *Client) AccessKey(ctx context.Context) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := c.do(ctx, "GET", "/v1/access-key", nil, nil, &out)
	return out.Key, err
}

// BoardKey returns the key a board served by this client accepts at sign-in.
// A remote client already holds it as its API key; the daemon never sends
// it over HTTP. A local client reads it through the socket.
func (c *Client) BoardKey(ctx context.Context) (string, error) {
	if c.remote {
		if c.apiKey == "" {
			return "", errors.New("the remote has no API key; set one to serve its board")
		}
		return c.apiKey, nil
	}
	return c.AccessKey(ctx)
}
