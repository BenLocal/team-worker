package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/benlocal/team-worker/internal/model"
)

func (c *Client) GetSession(ctx context.Context, team, name, user string) (model.Session, error) {
	var out model.Session
	return out, c.do(ctx, http.MethodGet, teamPath(team, "sessions", name), url.Values{"user": {user}}, nil, &out)
}

func (c *Client) SaveSession(ctx context.Context, session model.Session) (model.Session, error) {
	var out model.Session
	return out, c.do(ctx, http.MethodPut, teamPath(session.Team, "sessions", session.Agent), url.Values{"user": {session.User}}, session, &out)
}

// ClearSessions deletes the named agent's saved session for user, or every
// saved session in the team when name is empty, and returns how many.
func (c *Client) ClearSessions(ctx context.Context, team, name, user string) (int, error) {
	var out struct {
		Cleared int `json:"cleared"`
	}
	path := teamPath(team, "sessions")
	var query url.Values
	if name != "" {
		path, query = teamPath(team, "sessions", name), url.Values{"user": {user}}
	}
	return out.Cleared, c.do(ctx, http.MethodDelete, path, query, nil, &out)
}

func (c *Client) AgentExitedRun(ctx context.Context, team, name, runID string, code int) (model.AgentExit, error) {
	var out model.AgentExit
	return out, c.do(ctx, http.MethodPost, teamPath(team, "agents", name, "exit"), url.Values{"run_id": {runID}, "code": {strconv.Itoa(code)}}, struct{}{}, &out)
}

func (c *Client) RequestAgentStop(ctx context.Context, team, name, runID string) (model.Agent, error) {
	var out model.Agent
	return out, c.do(ctx, http.MethodPost, teamPath(team, "agents", name, "stop"), url.Values{"run_id": {runID}}, struct{}{}, &out)
}

// SetAgentActivity reports what an agent is doing for its run runID.
func (c *Client) SetAgentActivity(ctx context.Context, team, name, runID string, activity model.AgentActivity) error {
	body := map[string]string{"runId": runID, "activity": string(activity)}
	return c.do(ctx, http.MethodPost, teamPath(team, "agents", name, "activity"), nil, body, nil)
}

func (c *Client) StopTeamAgents(ctx context.Context, team string) ([]model.Agent, error) {
	var out []model.Agent
	return out, c.do(ctx, http.MethodPost, teamPath(team, "agents", "stop"), nil, struct{}{}, &out)
}
