// Package client talks to team-workerd over its Unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// APIError is an error reported by the daemon.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return e.Msg }

// IsConflict reports a lock or duplicate conflict from the daemon.
func IsConflict(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusConflict
}

// IsUnavailable reports that the daemon is stopping or not reachable, which
// long-running watchers treat as a reason to retry.
func IsUnavailable(err error) bool {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status == http.StatusServiceUnavailable
	}
	return isUnreachable(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type Client struct {
	base string
	http *http.Client
	// start launches the daemon when the socket is not answering; nil disables it.
	start func(context.Context) error
}

// New returns a client for the daemon serving dataDir. Unless
// TEAM_WORKER_NO_AUTOSTART is set, it starts team-workerd on first use.
func New(dataDir string) *Client {
	socket := config.SocketPath(dataDir)
	c := &Client{
		base: "http://team-workerd",
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
	if os.Getenv("TEAM_WORKER_NO_AUTOSTART") == "" {
		c.start = func(ctx context.Context) error { return startDaemon(ctx, c, dataDir) }
	}
	return c
}

// NewHTTP returns a client for an arbitrary base URL; tests use it with httptest.
func NewHTTP(base string, hc *http.Client) *Client { return &Client{base: base, http: hc} }

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	err := c.doOnce(ctx, method, path, query, body, out)
	if err != nil && c.start != nil && isUnreachable(err) {
		if startErr := c.start(ctx); startErr != nil {
			return startErr
		}
		err = c.doOnce(ctx, method, path, query, body, out)
	}
	return err
}

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("team-workerd returned %s", resp.Status)
		}
		return &APIError{Status: resp.StatusCode, Msg: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// startDaemon launches team-workerd detached from the CLI and waits for it
// to answer health checks. The binary is looked up next to the CLI first.
func startDaemon(ctx context.Context, c *Client, dataDir string) error {
	bin := "team-workerd"
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), "team-workerd"); fileExists(p) {
			bin = p
		}
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("team-workerd is not running and was not found in PATH: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(config.OutPath(dataDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	args := []string{"--data-dir", dataDir}
	// An explicit TEAM_WORKER_LOG_FILE wins; the daemon reads it itself.
	if os.Getenv("TEAM_WORKER_LOG_FILE") == "" {
		args = append(args, "--log-file", config.LogPath(dataDir))
	}
	cmd := exec.Command(path, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start team-workerd: %w", err)
	}
	go cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Health(ctx) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("team-workerd did not become ready; see %s and %s",
		config.LogPath(dataDir), config.OutPath(dataDir))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func teamPath(team string, parts ...string) string {
	p := "/v1/teams/" + url.PathEscape(team)
	for _, part := range parts {
		p += "/" + url.PathEscape(part)
	}
	return p
}

// Health checks the daemon without auto-starting it.
func (c *Client) Health(ctx context.Context) error {
	return c.doOnce(ctx, http.MethodGet, "/v1/health", nil, nil, nil)
}

func (c *Client) ListTeams(ctx context.Context) ([]model.TeamSummary, error) {
	var out []model.TeamSummary
	return out, c.do(ctx, http.MethodGet, "/v1/teams", nil, nil, &out)
}

func (c *Client) CreateTeam(ctx context.Context, in store.CreateTeamInput) (model.Team, error) {
	var out model.Team
	return out, c.do(ctx, http.MethodPost, "/v1/teams", nil, in, &out)
}

func (c *Client) GetTeam(ctx context.Context, team string) (model.Team, error) {
	var out model.Team
	return out, c.do(ctx, http.MethodGet, teamPath(team), nil, nil, &out)
}

func (c *Client) DeleteTeam(ctx context.Context, team string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	return out.Deleted, c.do(ctx, http.MethodDelete, teamPath(team), nil, nil, &out)
}

func (c *Client) AddMember(ctx context.Context, team string, in store.AddMemberInput) (model.Member, error) {
	var out model.Member
	return out, c.do(ctx, http.MethodPost, teamPath(team, "members"), nil, in, &out)
}

func (c *Client) Leader(ctx context.Context, team string) (model.Member, error) {
	var out model.Member
	return out, c.do(ctx, http.MethodGet, teamPath(team, "leader"), nil, nil, &out)
}

func (c *Client) CreateTask(ctx context.Context, team string, in store.CreateTaskInput) (model.Task, error) {
	var out model.Task
	return out, c.do(ctx, http.MethodPost, teamPath(team, "tasks"), nil, in, &out)
}

func (c *Client) GetTask(ctx context.Context, team, id string) (model.Task, error) {
	var out model.Task
	return out, c.do(ctx, http.MethodGet, teamPath(team, "tasks", id), nil, nil, &out)
}

func (c *Client) UpdateTask(ctx context.Context, team, id string, in store.UpdateTaskInput) (model.Task, error) {
	var out model.Task
	return out, c.do(ctx, http.MethodPatch, teamPath(team, "tasks", id), nil, in, &out)
}

func (c *Client) ListTasks(ctx context.Context, team string, f store.TaskFilter) ([]model.Task, error) {
	q := url.Values{}
	if f.Status != "" {
		q.Set("status", string(f.Status))
	}
	if f.Owner != "" {
		q.Set("owner", f.Owner)
	}
	if f.Priority != "" {
		q.Set("priority", string(f.Priority))
	}
	if f.SortPriority {
		q.Set("sort", "priority")
	}
	var out []model.Task
	return out, c.do(ctx, http.MethodGet, teamPath(team, "tasks"), q, nil, &out)
}

func (c *Client) TaskStats(ctx context.Context, team string) (model.TaskStats, error) {
	var out model.TaskStats
	return out, c.do(ctx, http.MethodGet, teamPath(team, "tasks", "stats"), nil, nil, &out)
}

func (c *Client) Send(ctx context.Context, team string, in store.SendInput) (model.Message, error) {
	var out model.Message
	return out, c.do(ctx, http.MethodPost, teamPath(team, "messages"), nil, in, &out)
}

func (c *Client) Broadcast(ctx context.Context, team string, in store.BroadcastInput) ([]model.Message, error) {
	var out []model.Message
	return out, c.do(ctx, http.MethodPost, teamPath(team, "broadcast"), nil, in, &out)
}

// Receive consumes messages; a positive wait long-polls until one arrives.
func (c *Client) Receive(ctx context.Context, team, agent, user string, limit int, wait time.Duration) ([]model.Message, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if user != "" {
		q.Set("user", user)
	}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	var out []model.Message
	return out, c.do(ctx, http.MethodPost, teamPath(team, "inboxes", agent, "receive"), q, nil, &out)
}

func (c *Client) Peek(ctx context.Context, team, agent, user string) ([]model.Message, error) {
	q := url.Values{}
	if user != "" {
		q.Set("user", user)
	}
	var out []model.Message
	return out, c.do(ctx, http.MethodGet, teamPath(team, "inboxes", agent), q, nil, &out)
}

func (c *Client) MessageLog(ctx context.Context, team string, limit int, from string) ([]model.Message, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if from != "" {
		q.Set("from", from)
	}
	var out []model.Message
	return out, c.do(ctx, http.MethodGet, teamPath(team, "messages", "log"), q, nil, &out)
}

func (c *Client) TeamPrompt(ctx context.Context, team string) (string, error) {
	var out struct {
		Prompt string `json:"prompt"`
	}
	return out.Prompt, c.do(ctx, http.MethodGet, teamPath(team, "prompt"), nil, nil, &out)
}

// SetTeamPrompt saves shared instructions and reports whether they changed.
func (c *Client) SetTeamPrompt(ctx context.Context, team, prompt string) (bool, error) {
	var out struct {
		Changed bool `json:"changed"`
	}
	body := map[string]string{"prompt": prompt}
	return out.Changed, c.do(ctx, http.MethodPut, teamPath(team, "prompt"), nil, body, &out)
}

func (c *Client) Board(ctx context.Context, team string, messages int) (model.Board, error) {
	var out model.Board
	q := url.Values{"messages": {strconv.Itoa(messages)}}
	return out, c.do(ctx, http.MethodGet, teamPath(team, "board"), q, nil, &out)
}

func (c *Client) RegisterAgent(ctx context.Context, a model.Agent) (model.Agent, error) {
	var out model.Agent
	return out, c.do(ctx, http.MethodPost, teamPath(a.Team, "agents"), nil, a, &out)
}

func (c *Client) ListAgents(ctx context.Context, team string) ([]model.Agent, error) {
	var out []model.Agent
	return out, c.do(ctx, http.MethodGet, teamPath(team, "agents"), nil, nil, &out)
}

func (c *Client) GetAgent(ctx context.Context, team, name string) (model.Agent, error) {
	var out model.Agent
	return out, c.do(ctx, http.MethodGet, teamPath(team, "agents", name), nil, nil, &out)
}

func (c *Client) AgentExited(ctx context.Context, team, name string) (model.AgentExit, error) {
	var out model.AgentExit
	return out, c.do(ctx, http.MethodPost, teamPath(team, "agents", name, "exit"), nil, struct{}{}, &out)
}

// IsNotFound reports a missing team, task or agent.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Changes returns the daemon's write version, waiting up to wait for it to
// move past after. A zero wait returns the current version at once.
func (c *Client) Changes(ctx context.Context, after uint64, wait time.Duration) (uint64, error) {
	q := url.Values{"after": {strconv.FormatUint(after, 10)}}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	var out struct {
		Version uint64 `json:"version"`
	}
	return out.Version, c.do(ctx, http.MethodGet, "/v1/changes", q, nil, &out)
}
