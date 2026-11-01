package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type fakeCLI struct {
	mu    sync.Mutex
	args  [][]string
	envs  [][]string
	out   string
	code  int
	block chan struct{}
}

func (f *fakeCLI) run(_ context.Context, _ string, args, env []string) ([]byte, int, error) {
	f.mu.Lock()
	f.args, f.envs = append(f.args, args), append(f.envs, env)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return []byte(f.out), f.code, nil
}

func newResumeFixture(t *testing.T, remote bool) (*Server, *store.Store, *fakeCLI) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := st.AddMember(ctx, "demo", store.AddMemberInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "bob", Backend: "tmux", Target: "%2", Host: "other-machine.example"}); err != nil {
		t.Fatal(err)
	}
	cli := &fakeCLI{out: `{"status":"spawned","agentName":"alice","target":"%9"}`}
	srv := New(st, nil)
	srv.EnableResume(&Resumer{Bin: "team-worker", Env: []string{"PATH=/usr/bin"}, Timeout: 5 * time.Second, Run: cli.run,
		busy: map[string]bool{}}, remote)
	return srv, st, cli
}

// post sends a resume request over the socket (local) or over HTTP.
func post(srv *Server, local bool, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if local {
		req = req.WithContext(context.WithValue(req.Context(), localAccess{}, true))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestResumeOverHTTPNeedsTheSwitch(t *testing.T) {
	srv, _, cli := newResumeFixture(t, false)
	if rec := post(srv, false, "/v1/teams/demo/agents/alice/resume", "{}"); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "--remote-resume") || len(cli.args) != 0 {
		t.Fatalf("HTTP resume without --remote-resume: %d %s", rec.Code, rec.Body)
	}
	if rec := post(srv, true, "/v1/teams/demo/agents/alice/resume", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("the socket may always resume: %d %s", rec.Code, rec.Body)
	}
}

func TestResumeRunsAgentResumeWithTheSavedSession(t *testing.T) {
	srv, _, cli := newResumeFixture(t, true)
	rec := post(srv, false, "/v1/teams/demo/agents/alice/resume?user=u1",
		`{"keepalive":true,"maxRestarts":2,"restartDelay":"10s"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"target":"%9"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := []string{"--json", "agent", "resume", "demo", "alice", "--replace", "--keepalive=true",
		"--max-restarts", "2", "--restart-delay", "10s"}
	if !slices.Equal(cli.args[0], want) || !slices.Contains(cli.envs[0], "TEAM_WORKER_USER=u1") {
		t.Fatalf("args %q env %q", cli.args[0], cli.envs[0])
	}
}

func TestResumeRejectsWhatItCannotDo(t *testing.T) {
	srv, _, cli := newResumeFixture(t, true)
	for _, tc := range []struct {
		path, body string
		code       int
		says       string
	}{
		// Only the keepalive policy can change; never the command.
		{"/v1/teams/demo/agents/alice/resume", `{"command":["sh","-c","id"]}`, http.StatusBadRequest, "unknown field"},
		{"/v1/teams/demo/agents/alice/resume", `{"restartDelay":"soon"}`, http.StatusBadRequest, "restartDelay"},
		{"/v1/teams/demo/agents/bob/resume", `{}`, http.StatusConflict, "other-machine.example"},
		{"/v1/teams/demo/agents/nobody/resume", `{}`, http.StatusNotFound, ""},
	} {
		rec := post(srv, false, tc.path, tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.says) {
			t.Fatalf("%s %s: %d %s", tc.path, tc.body, rec.Code, rec.Body)
		}
	}
	if len(cli.args) != 0 {
		t.Fatalf("nothing should have run: %q", cli.args)
	}

	cli.out, cli.code = `{"error":"no native session id was captured; use session save first"}`, 1
	if rec := post(srv, false, "/v1/teams/demo/agents/alice/resume", `{}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "no native session id") {
		t.Fatalf("the CLI's error should come back: %d %s", rec.Code, rec.Body)
	}
}

func TestResumeOneAgentAtATime(t *testing.T) {
	srv, _, cli := newResumeFixture(t, true)
	cli.block = make(chan struct{})
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- post(srv, false, "/v1/teams/demo/agents/alice/resume", `{}`) }()
	for {
		cli.mu.Lock()
		started := len(cli.args) == 1
		cli.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if rec := post(srv, false, "/v1/teams/demo/agents/alice/resume", `{}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "already running") {
		t.Fatalf("a second resume of the same agent: %d %s", rec.Code, rec.Body)
	}
	close(cli.block)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("first resume: %d %s", rec.Code, rec.Body)
	}
}

func TestResumerReachesThisDaemonOnly(t *testing.T) {
	t.Setenv("TEAM_WORKER_SERVER_URL", "http://elsewhere:8082")
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	t.Setenv("TEAM_WORKER_DATA_DIR", "/old")
	r := NewResumer("/data", "/data/team-workerd.sock")
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "TEAM_WORKER_SERVER_URL=") || strings.HasPrefix(kv, "TEAM_WORKER_AGENT_NAME=") || kv == "TEAM_WORKER_DATA_DIR=/old" {
			t.Fatalf("%s must not reach the resume CLI", kv)
		}
	}
	for _, want := range []string{"TEAM_WORKER_DATA_DIR=/data", "TEAM_WORKER_SOCKET=/data/team-workerd.sock", "TEAM_WORKER_NO_AUTOSTART=1"} {
		if !slices.Contains(r.Env, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
