package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/benlocal/team-worker/internal/model"
)

// ContextDiff is what an agent's work changes relative to its base branch.
func (c *Client) ContextDiff(ctx context.Context, team, agent, repo string) (model.AgentDiff, error) {
	var out model.AgentDiff
	return out, c.do(ctx, http.MethodGet, teamPath(team, "context", "diff", agent), url.Values{"repo": {repo}}, nil, &out)
}

// ContextFiles maps each file the team's agents changed to who changed it.
func (c *Client) ContextFiles(ctx context.Context, team, repo string) (map[string][]string, error) {
	var out map[string][]string
	return out, c.do(ctx, http.MethodGet, teamPath(team, "context", "files"), url.Values{"repo": {repo}}, nil, &out)
}

// ContextConflicts lists the files several agents changed.
func (c *Client) ContextConflicts(ctx context.Context, team, repo string) ([]model.FileOverlap, error) {
	var out []model.FileOverlap
	return out, c.do(ctx, http.MethodGet, teamPath(team, "context", "conflicts"), url.Values{"repo": {repo}}, nil, &out)
}

// ContextLog is the agents' commits across their branches, newest first.
func (c *Client) ContextLog(ctx context.Context, team, repo string, limit int) ([]model.AgentCommit, error) {
	var out []model.AgentCommit
	return out, c.do(ctx, http.MethodGet, teamPath(team, "context", "log"),
		url.Values{"repo": {repo}, "limit": {strconv.Itoa(limit)}}, nil, &out)
}

// ContextInject is the context block for an agent's prompt.
func (c *Client) ContextInject(ctx context.Context, team, agent, repo string) (string, error) {
	var out struct {
		Context string `json:"context"`
	}
	return out.Context, c.do(ctx, http.MethodGet, teamPath(team, "context", "inject", agent), url.Values{"repo": {repo}}, nil, &out)
}
