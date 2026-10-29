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
	sent    []map[string]any
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
	mux.HandleFunc("POST /v1/teams/{team}/messages", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		in := decode(r)
		d.sent = append(d.sent, in)
		reply(w, in)
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
	if got["backend"] != "tmux" || got["target"] != "%3" || got["cwd"] != "/workspace" || got["nudge"] != true {
		t.Fatalf("registered %v", got)
	}
}

func TestWatcherAnnouncesNewTasksThroughTheInbox(t *testing.T) {
	d, api := newFakeDaemon(t)
	d.teams["demo"] = true
	d.tasks = []map[string]any{{"id": "t0", "subject": "old", "priority": "low", "owner": ""}}
	d.inboxes["demo/leader"] = []map[string]any{{"type": "message", "from": "bob", "content": "hello"}}

	w := &watcher{api: api, team: "demo", leader: "leader", log: io.Discard}
	if err := w.round(); err != nil {
		t.Fatal(err)
	}
	if len(d.sent) != 0 {
		t.Fatalf("tasks present at start must not be announced: %v", d.sent)
	}
	if len(d.inboxes["demo/leader"]) != 1 {
		t.Fatal("the watcher must leave inboxes to the hooks and the daemon")
	}

	d.tasks = append(d.tasks,
		map[string]any{"id": "t1", "subject": "new job", "priority": "high", "owner": ""},
		map[string]any{"id": "t2", "subject": "assigned", "priority": "high", "owner": "bob"})
	for range 2 {
		if err := w.round(); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.sent) != 1 {
		t.Fatalf("want one announcement, got %v", d.sent)
	}
	msg := d.sent[0]
	content, _ := msg["content"].(string)
	if msg["to"] != "leader" || msg["from"] != "team-worker" || !strings.Contains(content, "- t1 [high] new job") ||
		strings.Contains(content, "t0") || strings.Contains(content, "t2") {
		t.Fatalf("announcement %v", msg)
	}
}
