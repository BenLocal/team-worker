package board

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/benlocal/team-worker/internal/client"
)

const (
	maxMessage      = 32 << 10
	maxHistoryLines = 5000
	maxHistoryBytes = 2 << 20
	maxTickets      = 128
	maxViewers      = 32
	ticketTTL       = 30 * time.Second
	pingInterval    = 20 * time.Second
	idleTimeout     = 60 * time.Second
	tmuxTimeout     = 3 * time.Second
)

// userError is an error whose text is safe to show in the browser.
type userError string

func (e userError) Error() string { return string(e) }

const errUnavailable = userError("Agent terminal is unavailable")

// pane is the tmux pane an agent runs in.
type pane struct {
	id      string // e.g. "%12"
	session string
	window  string // window id, e.g. "@3"
}

// resolver finds the live tmux pane of a team member; tests replace it.
type resolver func(ctx context.Context, team, agent string) (pane, error)

func tmuxRun(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tmuxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return string(out), nil
}

// paneInfo looks up a live pane; a missing or dead pane is unavailable.
func paneInfo(ctx context.Context, target string) (pane, error) {
	// tmux 3.3 prints control characters such as tabs in formats as "_", so
	// fields are space-separated with the session name, which may contain
	// spaces, last.
	out, err := tmuxRun(ctx, "display-message", "-p", "-t", target,
		"#{pane_id} #{pane_dead} #{window_id} #{session_name}")
	if err != nil {
		return pane{}, errUnavailable
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), " ", 4)
	if len(f) != 4 || f[0] != target || f[1] != "0" {
		return pane{}, errUnavailable
	}
	return pane{id: f[0], session: f[3], window: f[2]}, nil
}

// daemonResolver resolves panes from the agent records kept by team-workerd.
func daemonResolver(c *client.Client) resolver {
	return func(ctx context.Context, team, agent string) (pane, error) {
		a, err := c.GetAgent(ctx, team, agent)
		if err != nil {
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				return pane{}, userError("Agent has not been started")
			}
			return pane{}, errUnavailable
		}
		if a.Backend != "tmux" {
			return pane{}, userError("Live terminals require the tmux backend")
		}
		if a.Status != "running" || a.Target == "" {
			return pane{}, userError("Agent terminal is offline")
		}
		return paneInfo(ctx, a.Target)
	}
}

func validSize(cols, rows int) error {
	if cols < 20 || cols > 400 || rows < 5 || rows > 150 {
		return userError("Terminal size must be 20-400 columns and 5-150 rows")
	}
	return nil
}

// sameOrigin accepts browser requests whose Origin matches the Host they
// were sent to; behind nginx the Host header carries the public host.
func sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type ticket struct {
	team, agent string
	control     bool
	cols, rows  int
	expires     time.Time
}

// tickets are short-lived, single-use capabilities for one WebSocket.
type tickets struct {
	mu  sync.Mutex
	m   map[string]ticket
	now func() time.Time
}

func newTickets() *tickets { return &tickets{m: map[string]ticket{}, now: time.Now} }

func (ts *tickets) issue(t ticket) (string, error) {
	now := ts.now()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for k, v := range ts.m {
		if !v.expires.After(now) {
			delete(ts.m, k)
		}
	}
	if len(ts.m) >= maxTickets {
		return "", userError("Too many pending terminal connections")
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	t.expires = now.Add(ticketTTL)
	ts.m[token] = t
	return token, nil
}

func (ts *tickets) consume(token string) (ticket, error) {
	ts.mu.Lock()
	t, ok := ts.m[token]
	delete(ts.m, token)
	ts.mu.Unlock()
	if !ok || !t.expires.After(ts.now()) {
		return ticket{}, userError("Terminal ticket expired; reconnect to continue")
	}
	return t, nil
}

// terminals serves the ticket and WebSocket endpoints.
type terminals struct {
	resolve     resolver
	tickets     *tickets
	viewers     chan struct{}
	mu          sync.Mutex
	controllers map[[2]string]bool
}

func newTerminals(resolve resolver) *terminals {
	return &terminals{
		resolve:     resolve,
		tickets:     newTickets(),
		viewers:     make(chan struct{}, maxViewers),
		controllers: map[[2]string]bool{},
	}
}

// lockControl gives one viewer at a time the right to type into an agent.
func (t *terminals) lockControl(team, agent string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := [2]string{team, agent}
	if t.controllers[key] {
		return false
	}
	t.controllers[key] = true
	return true
}

func (t *terminals) unlockControl(team, agent string) {
	t.mu.Lock()
	delete(t.controllers, [2]string{team, agent})
	t.mu.Unlock()
}

func httpError(w http.ResponseWriter, status int, err error) {
	msg := "Agent terminal is unavailable"
	var ue userError
	if errors.As(err, &ue) {
		msg = ue.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (t *terminals) handleTicket(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		httpError(w, http.StatusForbidden, userError("Terminal requests require the same origin"))
		return
	}
	var in struct {
		Team    string `json:"team"`
		Agent   string `json:"agent"`
		Control bool   `json:"control"`
		Cols    *int   `json:"cols"`
		Rows    *int   `json:"rows"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessage)).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, userError("Invalid terminal request"))
		return
	}
	if in.Team == "" || in.Agent == "" {
		httpError(w, http.StatusBadRequest, userError("Team and agent are required"))
		return
	}
	cols, rows := 100, 30
	if in.Cols != nil {
		cols = *in.Cols
	}
	if in.Rows != nil {
		rows = *in.Rows
	}
	if err := validSize(cols, rows); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := t.resolve(r.Context(), in.Team, in.Agent); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	token, err := t.tickets.issue(ticket{team: in.Team, agent: in.Agent, control: in.Control, cols: cols, rows: rows})
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	reply(w, map[string]string{"ticket": token}, nil)
}

func (t *terminals) handleWS(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		httpError(w, http.StatusForbidden, userError("Terminal connections require the same origin"))
		return
	}
	tk, err := t.tickets.consume(r.URL.Query().Get("ticket"))
	if err != nil {
		httpError(w, http.StatusForbidden, err)
		return
	}
	p, err := t.resolve(r.Context(), tk.team, tk.agent)
	if err != nil {
		httpError(w, http.StatusForbidden, err)
		return
	}
	select {
	case t.viewers <- struct{}{}:
		defer func() { <-t.viewers }()
	default:
		httpError(w, http.StatusServiceUnavailable, userError("Too many live terminals"))
		return
	}
	if tk.control {
		if !t.lockControl(tk.team, tk.agent) {
			httpError(w, http.StatusConflict, userError("Another viewer is controlling this agent"))
			return
		}
		defer t.unlockControl(tk.team, tk.agent)
	}
	v, err := openViewer(r.Context(), p, tk)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err)
		return
	}
	defer v.close()
	// Origin was checked above against the Host nginx forwards.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessage)
	relay(r.Context(), conn, v, p, tk.control)
}

// viewer is a disposable tmux client attached to a session grouped with the
// agent's; closing it never terminates the agent's pane.
type viewer struct {
	name string
	cmd  *exec.Cmd
	pty  *os.File
}

func openViewer(ctx context.Context, p pane, tk ticket) (*viewer, error) {
	id := make([]byte, 12)
	rand.Read(id)
	v := &viewer{name: "team-worker-live-" + hex.EncodeToString(id)}
	steps := [][]string{
		{"new-session", "-d", "-s", v.name, "-t", "=" + p.session},
		{"set-option", "-t", v.name, "status", "off"},
		{"set-option", "-t", v.name, "prefix", "None"},
		{"set-hook", "-t", v.name, "client-detached", "kill-session -t " + v.name},
		{"select-window", "-t", v.name + ":" + p.window},
	}
	if tk.control {
		steps = append(steps, []string{"select-pane", "-t", p.id})
	}
	for _, args := range steps {
		if _, err := tmuxRun(ctx, args...); err != nil {
			v.close()
			return nil, errUnavailable
		}
	}
	flags := "ignore-size"
	if !tk.control {
		flags += ",read-only"
	}
	// -u: the board's environment usually has no UTF-8 locale, and without it the
	// tmux client draws every non-ASCII character (box drawing, CJK) as "_".
	cmd := exec.Command("tmux", "-u", "attach-session", "-f", flags, "-t", v.name)
	cmd.Env = append(attachEnv(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(tk.cols), Rows: uint16(tk.rows)})
	if err != nil {
		v.close()
		return nil, errUnavailable
	}
	v.cmd, v.pty = cmd, f
	return v, nil
}

// attachEnv drops the variables that would make tmux treat the viewer as
// nested inside another client.
func attachEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "TMUX" && name != "TMUX_PANE" && name != "TERM" {
			env = append(env, kv)
		}
	}
	return env
}

// detach ends the tmux client; the agent's pane keeps running.
func (v *viewer) detach() {
	if v.cmd != nil && v.cmd.Process != nil {
		v.cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (v *viewer) close() {
	if v.pty != nil {
		v.pty.Close()
	}
	if v.cmd != nil && v.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			v.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			v.cmd.Process.Kill()
			<-done
		}
	}
	tmuxRun(context.Background(), "kill-session", "-t", v.name)
}

// closeError ends the relay with a specific WebSocket close status.
type closeError struct {
	code   websocket.StatusCode
	reason string
}

func (e closeError) Error() string { return e.reason }

type clientMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Data string `json:"data"`
}

type historyMessage struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// truncateHistory keeps the newest maxHistoryBytes, cut at a line start so
// UTF-8 sequences and color escapes stay intact.
func truncateHistory(text string) (string, bool) {
	if len(text) <= maxHistoryBytes {
		return text, false
	}
	tail := text[len(text)-maxHistoryBytes:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	} else {
		tail = ""
	}
	return tail, true
}

// captureHistory reads scrollback without putting the shared pane into copy mode.
func captureHistory(ctx context.Context, p pane) historyMessage {
	out, err := tmuxRun(ctx, "capture-pane", "-p", "-e", "-J", "-S", fmt.Sprintf("-%d", maxHistoryLines), "-t", p.id)
	if err != nil {
		return historyMessage{Type: "history", Error: "Agent history is unavailable"}
	}
	text, truncated := truncateHistory(out)
	return historyMessage{Type: "history", Text: text, Truncated: truncated}
}

// relay copies PTY output to the socket and applies the client's resize,
// history and input messages until either side ends.
func relay(ctx context.Context, conn *websocket.Conn, v *viewer, p pane, control bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	input := make(chan []byte, 64)
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- fn()
		}()
	}

	run(func() error {
		buf := make([]byte, 16<<10)
		for {
			n, err := v.pty.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return werr
				}
			}
			if err != nil {
				return closeError{websocket.StatusNormalClosure, "Agent terminal closed"}
			}
		}
	})
	run(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case data := <-input:
				if _, err := v.pty.Write(data); err != nil {
					return err
				}
			}
		}
	})
	run(func() error {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				pctx, pcancel := context.WithTimeout(ctx, pingInterval/2)
				if conn.Ping(pctx) == nil {
					lastRead.Store(time.Now().UnixNano())
				}
				pcancel()
				if time.Since(time.Unix(0, lastRead.Load())) > idleTimeout {
					return errors.New("terminal client went away")
				}
			}
		}
	})
	run(func() error {
		var history *historyMessage
		var historyAt time.Time
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
					return closeError{websocket.StatusMessageTooBig, "Invalid terminal message"}
				}
				return err
			}
			lastRead.Store(time.Now().UnixNano())
			var msg clientMessage
			if typ != websocket.MessageText || json.Unmarshal(data, &msg) != nil {
				return closeError{websocket.StatusMessageTooBig, "Invalid terminal message"}
			}
			switch msg.Type {
			case "resize":
				if validSize(msg.Cols, msg.Rows) != nil {
					return closeError{websocket.StatusPolicyViolation, "Invalid terminal size"}
				}
				if err := pty.Setsize(v.pty, &pty.Winsize{Cols: uint16(msg.Cols), Rows: uint16(msg.Rows)}); err != nil {
					return err
				}
			case "history":
				if history == nil || time.Since(historyAt) > time.Second {
					h := captureHistory(ctx, p)
					history, historyAt = &h, time.Now()
				}
				raw, _ := json.Marshal(history)
				if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
					return err
				}
			case "input":
				if !control || msg.Data == "" {
					continue
				}
				select {
				case input <- bytes.Clone([]byte(msg.Data)):
				default:
					return closeError{websocket.StatusPolicyViolation, "Terminal input buffer is full"}
				}
			}
		}
	})

	err := <-errs
	var ce closeError
	if errors.As(err, &ce) {
		conn.Close(ce.code, ce.reason)
	}
	cancel()
	// Ending the tmux client closes the PTY's other side, which is what
	// reliably unblocks the output reader.
	v.detach()
	conn.CloseNow()
	wg.Wait()
}
