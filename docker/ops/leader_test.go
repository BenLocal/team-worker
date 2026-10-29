package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon serves the team-workerd routes the leader commands use.
type fakeDaemon struct {
	mu      sync.Mutex
	teams   map[string]bool
	agents  []map[string]any
	inboxes map[string][]map[string]any
	tasks   []map[string]any
	created []map[string]any
	reg     []map[string]any
}

func newFakeDaemon(t *testing.T) (*fakeDaemon, *teamAPI) {
	d := &fakeDaemon{teams: map[string]bool{}, inboxes: map[string][]map[string]any{}}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	decode := func(r *http.Request) map[string]any {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		return m
	}
	mux.HandleFunc("GET /v1/teams", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		var out []map[string]string
		for name := range d.teams {
			out = append(out, map[string]string{"name": name})
		}
		reply(w, out)
	})
	mux.HandleFunc("POST /v1/teams", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		in := decode(r)
		d.created = append(d.created, in)
		d.teams[in["name"].(string)] = true
		reply(w, in)
	})
	mux.HandleFunc("GET /v1/teams/{team}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if !d.teams[r.PathValue("team")] {
			w.WriteHeader(http.StatusNotFound)
			reply(w, map[string]string{"error": "Team not found"})
			return
		}
		reply(w, map[string]string{"name": r.PathValue("team")})
	})
	mux.HandleFunc("GET /v1/teams/{team}/leader", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"name": "leader", "agentId": "abc123"})
	})
	mux.HandleFunc("GET /v1/teams/{team}/agents", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		reply(w, d.agents)
	})
	mux.HandleFunc("POST /v1/teams/{team}/agents", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.reg = append(d.reg, decode(r))
		reply(w, map[string]string{})
	})
	mux.HandleFunc("GET /v1/teams/{team}/agents/{name}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.agents {
			if a["name"] == r.PathValue("name") {
				reply(w, a)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		reply(w, map[string]string{"error": "no agent"})
	})
	mux.HandleFunc("POST /v1/teams/{team}/inboxes/{agent}/receive", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		key := r.PathValue("team") + "/" + r.PathValue("agent")
		msgs := d.inboxes[key]
		delete(d.inboxes, key)
		if msgs == nil {
			msgs = []map[string]any{}
		}
		reply(w, msgs)
	})
	mux.HandleFunc("GET /v1/teams/{team}/tasks", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		reply(w, d.tasks)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return d, &teamAPI{base: srv.URL, client: srv.Client()}
}

func TestPrepareCreatesMissingTeamOnce(t *testing.T) {
	d, api := newFakeDaemon(t)
	for range 2 {
		name, id, err := api.prepare("demo")
		if err != nil || name != "leader" || id != "abc123" {
			t.Fatalf("prepare = %q %q %v", name, id, err)
		}
	}
	if len(d.created) != 1 || d.created[0]["leaderName"] != "leader" {
		t.Fatalf("created = %v", d.created)
	}
}

func TestRegisterRecordsTmuxPane(t *testing.T) {
	d, api := newFakeDaemon(t)
	if err := api.register("demo", "leader", "abc123", "%3", "/workspace"); err != nil {
		t.Fatal(err)
	}
	got := d.reg[0]
	if got["backend"] != "tmux" || got["target"] != "%3" || got["cwd"] != "/workspace" {
		t.Fatalf("registered %v", got)
	}
}

func TestWatcherDeliversInboxesAndAnnouncesNewTasks(t *testing.T) {
	d, api := newFakeDaemon(t)
	alive, dead := true, false
	d.teams["demo"] = true
	d.agents = []map[string]any{
		{"name": "leader", "backend": "tmux", "target": "%1", "status": "running", "alive": alive},
		{"name": "gone", "backend": "tmux", "target": "%2", "status": "running", "alive": dead},
		{"name": "bg", "backend": "subprocess", "target": "", "status": "running", "alive": alive},
	}
	d.tasks = []map[string]any{{"id": "t0", "subject": "old", "priority": "low", "owner": ""}}
	d.inboxes["demo/leader"] = []map[string]any{{"type": "message", "from": "bob", "content": "hello", "requestId": "r1"}}
	d.inboxes["demo/gone"] = []map[string]any{{"from": "bob", "content": "lost"}}

	var pastes []string
	w := &watcher{api: api, team: "demo", leader: "leader", log: io.Discard,
		paste: func(pane, text string) error {
			pastes = append(pastes, pane+" "+text)
			return nil
		}}
	if err := w.round(); err != nil {
		t.Fatal(err)
	}
	if len(pastes) != 1 || pastes[0] != "%1 [team-worker] demo: message from bob\nhello" {
		t.Fatalf("first round pasted %q", pastes)
	}
	if d.inboxes["demo/gone"] == nil {
		t.Fatal("inbox of a dead pane was consumed")
	}

	pastes = nil
	d.tasks = append(d.tasks,
		map[string]any{"id": "t1", "subject": "new job", "priority": "high", "owner": ""},
		map[string]any{"id": "t2", "subject": "assigned", "priority": "high", "owner": "bob"})
	for range 2 {
		if err := w.round(); err != nil {
			t.Fatal(err)
		}
	}
	if len(pastes) != 1 || !strings.Contains(pastes[0], "- t1 [high] new job") ||
		strings.Contains(pastes[0], "t0") || strings.Contains(pastes[0], "t2") {
		t.Fatalf("task announcement %q", pastes)
	}
}

func TestFormatMessageShowsRequestIDForLifecycleTypes(t *testing.T) {
	got := formatMessage("demo", message{Type: "shutdown_request", From: "leader", RequestID: "r9", Reason: "done"})
	if got != "[team-worker] demo: shutdown_request from leader (request r9)\ndone" {
		t.Fatalf("got %q", got)
	}
}
