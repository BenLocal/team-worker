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
	"os/exec"
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
		Short: "Register the container leader and deliver team events into agent panes",
		Long: "team-worker inboxes only store messages; an interactive agent idling at its\n" +
			"prompt never reads them. The watcher consumes the inbox of every live tmux\n" +
			"agent, pastes each message into its pane, and tells the leader about new\n" +
			"unassigned pending tasks.",
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
			Short: "Deliver inbox messages and new unassigned tasks into agent panes",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				w := &watcher{api: newTeamAPI(), paste: tmuxPaste, team: args[0], leader: args[1], log: cmd.ErrOrStderr()}
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

type agentInfo struct {
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Target  string `json:"target"`
	Status  string `json:"status"`
	Alive   *bool  `json:"alive"`
}

type message struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
	Summary   string `json:"summary"`
	Reason    string `json:"reason"`
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
	}, nil)
}

// tmuxPaste types text into a pane as one bracketed paste and submits it.
func tmuxPaste(pane, text string) error {
	f, err := os.CreateTemp("", "team-worker-watch-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	buffer := "team-worker-watch-" + strings.TrimPrefix(pane, "%")
	if err := exec.Command("tmux", "load-buffer", "-b", buffer, f.Name()).Run(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}
	if err := exec.Command("tmux", "paste-buffer", "-d", "-p", "-r", "-b", buffer, "-t", pane).Run(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w", err)
	}
	// Let the TUI finish handling the bracketed paste before submitting it.
	time.Sleep(500 * time.Millisecond)
	if err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}

type watcher struct {
	api    *teamAPI
	paste  func(pane, text string) error
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
	var teams []struct {
		Name string `json:"name"`
	}
	if err := w.api.call(http.MethodGet, "/v1/teams", nil, &teams); err != nil {
		return err
	}
	for _, t := range teams {
		var agents []agentInfo
		if err := w.api.call(http.MethodGet, teamPath(t.Name, "agents"), nil, &agents); err != nil {
			return err
		}
		for _, a := range agents {
			if a.Backend == "tmux" && a.Status == "running" && a.Alive != nil && *a.Alive {
				if err := w.deliverInbox(t.Name, a); err != nil {
					return err
				}
			}
		}
	}
	return w.announceTasks()
}

func (w *watcher) deliverInbox(team string, a agentInfo) error {
	for {
		var msgs []message
		if err := w.api.call(http.MethodPost, teamPath(team, "inboxes", a.Name, "receive"), nil, &msgs); err != nil {
			return err
		}
		if len(msgs) == 0 {
			return nil
		}
		for _, m := range msgs {
			if err := w.paste(a.Target, formatMessage(team, m)); err != nil {
				return err
			}
		}
	}
}

func formatMessage(team string, m message) string {
	kind := m.Type
	if kind == "" {
		kind = "message"
	}
	from := m.From
	if from == "" {
		from = "unknown"
	}
	head := fmt.Sprintf("[team-worker] %s: %s from %s", team, kind, from)
	// Lifecycle requests are answered by ID, so show it; plain messages carry
	// an internal one that would only be noise.
	if m.RequestID != "" && kind != "message" {
		head += fmt.Sprintf(" (request %s)", m.RequestID)
	}
	for _, body := range []string{m.Content, m.Summary, m.Reason} {
		if body != "" {
			return head + "\n" + body
		}
	}
	return head
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
		var leader agentInfo
		if err := w.api.call(http.MethodGet, teamPath(w.team, "agents", w.leader), nil, &leader); err != nil {
			return err
		}
		lines := []string{fmt.Sprintf("[team-worker] %s: new unassigned pending tasks", w.team)}
		for _, t := range fresh {
			lines = append(lines, fmt.Sprintf("- %s [%s] %s", t.ID, t.Priority, t.Subject))
		}
		lines = append(lines, fmt.Sprintf("Check `team-worker task list %s --status pending`, then assign, split or take them.", w.team))
		if err := w.paste(leader.Target, strings.Join(lines, "\n")); err != nil {
			return err
		}
	}
	w.seen = pending
	return nil
}
