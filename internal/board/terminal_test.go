package board

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTicketsAreSingleUseAndExpire(t *testing.T) {
	ts := newTickets()
	now := time.Unix(1000, 0)
	ts.now = func() time.Time { return now }

	token, err := ts.issue(ticket{team: "demo", agent: "alice", cols: 80, rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ts.consume(token)
	if err != nil || got.agent != "alice" {
		t.Fatalf("consume = %+v, %v", got, err)
	}
	if _, err := ts.consume(token); err == nil {
		t.Fatal("ticket was accepted twice")
	}

	token, _ = ts.issue(ticket{team: "demo", agent: "alice"})
	now = now.Add(ticketTTL)
	if _, err := ts.consume(token); err == nil {
		t.Fatal("expired ticket was accepted")
	}
}

func TestTicketsLimitPendingAndPruneExpired(t *testing.T) {
	ts := newTickets()
	now := time.Unix(1000, 0)
	ts.now = func() time.Time { return now }
	for i := range maxTickets {
		if _, err := ts.issue(ticket{}); err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
	}
	if _, err := ts.issue(ticket{}); err == nil {
		t.Fatal("issued more than the pending limit")
	}
	now = now.Add(ticketTTL)
	if _, err := ts.issue(ticket{}); err != nil {
		t.Fatalf("expired tickets were not pruned: %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://board.local:8080", "board.local:8080", true},
		{"https://Board.Local", "board.local", true},
		{"http://evil.example", "board.local:8080", false},
		{"http://board.local:9090", "board.local:8080", false},
		{"", "board.local", false},
		{"file://board.local", "board.local", false},
		{"null", "board.local", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := sameOrigin(r); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v", c.origin, c.host, got)
		}
	}
}

func TestValidSize(t *testing.T) {
	for _, s := range [][2]int{{20, 5}, {400, 150}, {100, 30}} {
		if validSize(s[0], s[1]) != nil {
			t.Errorf("%v rejected", s)
		}
	}
	for _, s := range [][2]int{{19, 30}, {401, 30}, {100, 4}, {100, 151}, {0, 0}} {
		if validSize(s[0], s[1]) == nil {
			t.Errorf("%v accepted", s)
		}
	}
}

func TestTruncateHistoryCutsAtLineStart(t *testing.T) {
	short := "a\nb\n"
	if got, cut := truncateHistory(short); got != short || cut {
		t.Fatalf("short history changed: %q %v", got, cut)
	}
	line := strings.Repeat("界", 100) + "\n"
	long := strings.Repeat(line, maxHistoryBytes/len(line)+10) + "last\n"
	got, cut := truncateHistory(long)
	if !cut || len(got) > maxHistoryBytes {
		t.Fatalf("len %d truncated %v", len(got), cut)
	}
	if !strings.HasPrefix(got, "界") || !strings.HasSuffix(got, "last\n") {
		t.Fatalf("not cut at a line start: %q...%q", got[:12], got[len(got)-8:])
	}
}

func TestControlLockAllowsOneController(t *testing.T) {
	terms := newTerminals(nil)
	if !terms.lockControl("demo", "alice") {
		t.Fatal("first lock failed")
	}
	if terms.lockControl("demo", "alice") {
		t.Fatal("second controller got the lock")
	}
	if !terms.lockControl("demo", "bob") {
		t.Fatal("lock is not per agent")
	}
	terms.unlockControl("demo", "alice")
	if !terms.lockControl("demo", "alice") {
		t.Fatal("lock was not released")
	}
}

func stubResolver(p pane, err error) resolver {
	return func(context.Context, string, string) (pane, error) { return p, err }
}

func postTicket(t *testing.T, base string, body any, origin string) (*http.Response, map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/api/terminal/ticket", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestTicketEndpointBoundaries(t *testing.T) {
	srv := httptest.NewServer(newHandler(nil, stubResolver(pane{id: "%1"}, nil)))
	defer srv.Close()

	ok := map[string]any{"team": "demo", "agent": "alice", "cols": 80, "rows": 24}
	if resp, _ := postTicket(t, srv.URL, ok, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no origin: %d", resp.StatusCode)
	}
	if resp, _ := postTicket(t, srv.URL, ok, "http://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin: %d", resp.StatusCode)
	}
	bad := map[string]any{"team": "demo", "agent": "alice", "cols": 10, "rows": 24}
	if resp, out := postTicket(t, srv.URL, bad, srv.URL); resp.StatusCode != http.StatusBadRequest || out["error"] == "" {
		t.Fatalf("bad size: %d %v", resp.StatusCode, out)
	}
	notBool := map[string]any{"team": "demo", "agent": "alice", "control": "yes"}
	if resp, _ := postTicket(t, srv.URL, notBool, srv.URL); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-boolean control: %d", resp.StatusCode)
	}
	resp, out := postTicket(t, srv.URL, ok, srv.URL)
	if resp.StatusCode != http.StatusOK || out["ticket"] == "" {
		t.Fatalf("issue: %d %v", resp.StatusCode, out)
	}

	offline := httptest.NewServer(newHandler(nil, stubResolver(pane{}, userError("Agent terminal is offline"))))
	defer offline.Close()
	if resp, out := postTicket(t, offline.URL, ok, offline.URL); resp.StatusCode != http.StatusBadRequest || out["error"] != "Agent terminal is offline" {
		t.Fatalf("offline: %d %v", resp.StatusCode, out)
	}
}

func TestWebSocketRejectsMissingOrReusedTicket(t *testing.T) {
	srv := httptest.NewServer(newHandler(nil, stubResolver(pane{id: "%1"}, nil)))
	defer srv.Close()
	ctx := context.Background()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {srv.URL}}}
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/terminal/ws?ticket=nope", opts)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid ticket: err=%v resp=%v", err, resp)
	}
}

// tmuxServer starts an isolated tmux server running one agent-like pane.
func tmuxServer(t *testing.T) pane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "twt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	t.Cleanup(func() {
		exec.Command("tmux", "kill-server").Run()
		os.RemoveAll(dir)
	})
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", "team-worker-demo", "-n", "alice",
		"-x", "80", "-y", "24", "sh -c 'echo hello-from-agent; exec cat'").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v %s", err, out)
	}
	id, err := tmuxRun(context.Background(), "display-message", "-p", "-t", "team-worker-demo:alice", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	p, err := paneInfo(context.Background(), strings.TrimSpace(id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dialTerminal(t *testing.T, base string, control bool) *websocket.Conn {
	t.Helper()
	resp, out := postTicket(t, base, map[string]any{"team": "demo", "agent": "alice", "control": control, "cols": 80, "rows": 24}, base)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket: %d %v", resp.StatusCode, out)
	}
	conn, _, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(base, "http")+"/api/terminal/ws?ticket="+out["ticket"],
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {base}}})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// readUntil collects binary output until it contains want or times out, and
// returns the last size message seen on the way.
func readUntil(t *testing.T, conn *websocket.Conn, want string) (size sizeMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var seen bytes.Buffer
	for !strings.Contains(seen.String(), want) {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; got %q", want, err, seen.String())
		}
		if typ == websocket.MessageBinary {
			seen.Write(data)
		} else if json.Unmarshal(data, &size); size.Type != "size" {
			size = sizeMessage{}
		}
	}
	return size
}

func sendJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := conn.Write(context.Background(), websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

func sessions(t *testing.T) []string {
	t.Helper()
	out, _ := tmuxRun(context.Background(), "list-sessions", "-F", "#{session_name}")
	return strings.Fields(out)
}

func TestLiveTerminalRelaysOutputHistoryAndControlledInput(t *testing.T) {
	p := tmuxServer(t)
	srv := httptest.NewServer(newHandler(nil, func(ctx context.Context, _, _ string) (pane, error) {
		return paneInfo(ctx, p.id)
	}))
	defer srv.Close()

	watch := dialTerminal(t, srv.URL, false)
	size := readUntil(t, watch, "hello-from-agent")
	sendJSON(t, watch, map[string]any{"type": "resize", "cols": 100, "rows": 30})
	sendJSON(t, watch, map[string]any{"type": "input", "data": "ignored-input\r"})
	sendJSON(t, watch, map[string]any{"type": "history"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, data, err := watch.Read(ctx)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var h historyMessage
		json.Unmarshal(data, &h)
		if h.Type == "size" {
			json.Unmarshal(data, &size)
			continue
		}
		if h.Type != "history" || !strings.Contains(h.Text, "hello-from-agent") {
			t.Fatalf("history message %s", data)
		}
		break
	}
	// The first size message reports the agent window as created (80x24).
	if size.Cols != 80 || size.Rows != 24 {
		t.Fatalf("size message %+v, want the agent window's 80x24", size)
	}

	control := dialTerminal(t, srv.URL, true)
	_, second := postTicket(t, srv.URL, map[string]any{"team": "demo", "agent": "alice", "control": true}, srv.URL)
	conflict, resp, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/api/terminal/ws?ticket="+second["ticket"],
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {srv.URL}}})
	if err == nil {
		conflict.CloseNow()
		t.Fatal("a second controller connected")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("second controller: %v %v", resp, err)
	}

	sendJSON(t, control, map[string]any{"type": "input", "data": "typed-by-control\r"})
	readUntil(t, control, "typed-by-control")
	screen, _ := tmuxRun(context.Background(), "capture-pane", "-p", "-t", p.id)
	if strings.Contains(screen, "ignored-input") {
		t.Fatalf("watch-only input reached the pane:\n%s", screen)
	}

	watch.Close(websocket.StatusNormalClosure, "")
	control.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		names := sessions(t)
		if len(names) == 1 && names[0] == "team-worker-demo" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("viewer sessions left behind: %v", names)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := paneInfo(context.Background(), p.id); err != nil {
		t.Fatal("closing a viewer killed the agent pane")
	}
}

func TestLiveTerminalRejectsBinaryMessages(t *testing.T) {
	p := tmuxServer(t)
	srv := httptest.NewServer(newHandler(nil, func(ctx context.Context, _, _ string) (pane, error) {
		return paneInfo(ctx, p.id)
	}))
	defer srv.Close()
	conn := dialTerminal(t, srv.URL, false)
	defer conn.CloseNow()
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
				t.Fatalf("close status %v (%v)", got, err)
			}
			return
		}
	}
}

func TestPageIsServedFromTheEmbeddedBuild(t *testing.T) {
	if _, err := fs.Stat(dist, "dist/index.html"); err != nil {
		t.Skip("board page not built; run `make web`")
	}
	srv := httptest.NewServer(newHandler(nil, nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("GET / = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
}
