package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/store"
)

func newLoggedServer(t *testing.T, level slog.Level) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
	ts := httptest.NewServer(New(st, log))
	t.Cleanup(ts.Close)
	return ts, &buf
}

func call(t *testing.T, ts *httptest.Server, method, path, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestStateChangesAreLogged(t *testing.T) {
	ts, buf := newLoggedServer(t, slog.LevelInfo)
	call(t, ts, "POST", "/v1/teams", `{"name":"demo","leaderName":"leader"}`)
	call(t, ts, "POST", "/v1/teams/demo/members", `{"name":"alice"}`)
	call(t, ts, "POST", "/v1/teams/demo/tasks", `{"subject":"build","owner":"alice"}`)
	var id string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "task created") {
			id = strings.Fields(strings.SplitN(line, "task=", 2)[1])[0]
		}
	}
	call(t, ts, "PATCH", "/v1/teams/demo/tasks/"+id, `{"status":"in_progress","caller":"alice"}`)
	call(t, ts, "POST", "/v1/teams/demo/messages", `{"from":"alice","to":"leader","content":"secret plan"}`)

	out := buf.String()
	for _, want := range []string{`msg="team created" team=demo`, `msg="member added"`, `msg="task created"`,
		`status="pending -> in_progress"`, "by=alice", `lock=" -> alice"`, `msg="message sent"`, "chars=11"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret plan") {
		t.Error("message content must not be logged")
	}
	if strings.Contains(out, `msg=request `) {
		t.Error("successful requests belong at debug level")
	}
}

func TestRequestLogLevels(t *testing.T) {
	ts, buf := newLoggedServer(t, slog.LevelDebug)
	call(t, ts, "GET", "/v1/teams/missing", "")
	if !strings.Contains(buf.String(), `level=DEBUG msg=request method=GET path=/v1/teams/missing status=404`) {
		t.Fatalf("a GET that finds nothing is routine:\n%s", buf.String())
	}
	call(t, ts, "POST", "/v1/teams/missing/tasks", `{"subject":"x"}`)
	if !strings.Contains(buf.String(), `level=WARN msg="request rejected" method=POST path=/v1/teams/missing/tasks status=404`) ||
		!strings.Contains(buf.String(), `error="Team 'missing' not found"`) {
		t.Fatalf("a rejected write is a warning:\n%s", buf.String())
	}

	buf.Reset()
	call(t, ts, "POST", "/v1/teams", `{"name":"demo","leaderName":"leader"}`)
	old := SlowRequest
	SlowRequest = 50 * time.Millisecond
	defer func() { SlowRequest = old }()
	call(t, ts, "POST", "/v1/teams/demo/inboxes/leader/receive?wait=200ms", "")
	if strings.Contains(buf.String(), "slow request") || !strings.Contains(buf.String(), `level=DEBUG msg=request`) {
		t.Fatalf("a long poll is not slow:\n%s", buf.String())
	}
}
