package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// watchInterval is how often the watcher polls team-workerd; tests shorten it.
var watchInterval = 2 * time.Second

func leaderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leader",
		Short: "Register the container leader and tell it about new unassigned tasks",
		Long: "team-workerd and the agents' hooks deliver inbox messages themselves. The\n" +
			"watcher only adds what they do not cover: when a pending task without an\n" +
			"owner appears, it sends the leader an inbox message about it.",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "prepare TEAM",
			Short: "Create the team when missing and print the leader's name and agent ID",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name, id, err := newTeamAPI().prepare(args[0])
				if err != nil {
					return failWith("leader prepare failed: ", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), name, id)
				return nil
			},
		},
		&cobra.Command{
			Use:   "register TEAM NAME AGENT_ID PANE CWD",
			Short: "Record the leader's tmux pane so the board and the reaper can see it",
			Args:  cobra.ExactArgs(5),
			RunE: func(cmd *cobra.Command, args []string) error {
				err := newTeamAPI().register(args[0], args[1], args[2], args[3], args[4])
				return failWith("leader register failed: ", err)
			},
		},
		&cobra.Command{
			Use:   "watch TEAM LEADER",
			Short: "Send the leader an inbox message for each new unassigned pending task",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				w := &watcher{api: newTeamAPI(), team: args[0], leader: args[1], log: cmd.ErrOrStderr()}
				w.run(cmd.Context())
				return nil
			},
		},
	)
	return cmd
}

func teamWorkerSocket() string {
	if s := os.Getenv("TEAM_WORKER_SOCKET"); s != "" {
		return s
	}
	dataDir := os.Getenv("TEAM_WORKER_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(homeDir(), ".team-worker")
	}
	return filepath.Join(dataDir, "team-workerd.sock")
}

// apiError is an error response from team-workerd.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// teamAPI is a minimal team-workerd client over its Unix socket.
type teamAPI struct {
	base   string
	client *http.Client
}

func newTeamAPI() *teamAPI {
	sock := teamWorkerSocket()
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &teamAPI{base: "http://team-workerd", client: &http.Client{Timeout: 10 * time.Second, Transport: transport}}
}

func (a *teamAPI) call(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &apiError{status: resp.StatusCode, msg: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func teamPath(team string, parts ...string) string {
	p := "/v1/teams/" + url.PathEscape(team)
	for _, part := range parts {
		p += "/" + url.PathEscape(part)
	}
	return p
}

type member struct {
	Name    string `json:"name"`
	AgentID string `json:"agentId"`
}

type task struct {
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Priority string `json:"priority"`
	Owner    string `json:"owner"`
}

func (a *teamAPI) prepare(team string) (string, string, error) {
	err := a.call(http.MethodGet, teamPath(team), nil, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		id := make([]byte, 6)
		rand.Read(id)
		err = a.call(http.MethodPost, "/v1/teams", map[string]string{
			"name":            team,
			"description":     "team-worker Docker team",
			"leaderName":      "leader",
			"leaderId":        hex.EncodeToString(id),
			"leaderAgentType": "leader",
		}, nil)
	}
	if err != nil {
		return "", "", err
	}
	var leader member
	if err := a.call(http.MethodGet, teamPath(team, "leader"), nil, &leader); err != nil {
		return "", "", err
	}
	return leader.Name, leader.AgentID, nil
}

func (a *teamAPI) register(team, name, agentID, pane, cwd string) error {
	return a.call(http.MethodPost, teamPath(team, "agents"), map[string]any{
		"name":    name,
		"agentId": agentID,
		"backend": "tmux",
		"target":  pane,
		"cwd":     cwd,
		"command": []string{"leader-session", "claude"},
		// Lets team-workerd type a reminder when messages wait at an idle prompt.
		"nudge": true,
	}, nil)
}

type watcher struct {
	api    *teamAPI
	team   string
	leader string
	log    io.Writer
	// seen holds the unassigned pending task IDs from the previous round;
	// nil until the first round, so tasks present at start are not announced.
	seen map[string]bool
}

func (w *watcher) run(ctx context.Context) {
	for {
		if err := w.round(); err != nil {
			// The daemon or a pane may be restarting; retry on the next round.
			fmt.Fprintln(w.log, "leader watch:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchInterval):
		}
	}
}

func (w *watcher) round() error {
	return w.announceTasks()
}

func (w *watcher) announceTasks() error {
	var tasks []task
	if err := w.api.call(http.MethodGet, teamPath(w.team, "tasks")+"?status=pending", nil, &tasks); err != nil {
		return err
	}
	pending := map[string]bool{}
	var fresh []task
	for _, t := range tasks {
		if t.Owner != "" {
			continue
		}
		pending[t.ID] = true
		// The leader reads the task list on start, so only later arrivals count.
		if w.seen != nil && !w.seen[t.ID] {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) > 0 {
		lines := []string{"New unassigned pending tasks:"}
		for _, t := range fresh {
			lines = append(lines, fmt.Sprintf("- %s [%s] %s", t.ID, t.Priority, t.Subject))
		}
		lines = append(lines, fmt.Sprintf("Check `team-worker task list %s --status pending`, then assign, split or take them.", w.team))
		// An inbox message reaches the leader through its hooks or the daemon's
		// reminder, at a moment it can act on it.
		if err := w.api.call(http.MethodPost, teamPath(w.team, "messages"), map[string]string{
			"from": "team-worker", "to": w.leader, "type": "message", "content": strings.Join(lines, "\n"),
		}, nil); err != nil {
			return err
		}
	}
	w.seen = pending
	return nil
}
