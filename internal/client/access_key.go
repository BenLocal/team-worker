package client

import "context"

// AccessKey retrieves the shared key through the owner-only Unix socket.
func (c *Client) AccessKey(ctx context.Context) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := c.do(ctx, "GET", "/v1/access-key", nil, nil, &out)
	return out.Key, err
}
