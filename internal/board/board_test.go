package board

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
)

// promptStub is a minimal daemon exposing the prompt API the board proxies.
// Its mutex keeps the handler goroutines safe from the test writing prompts.
type promptStub struct {
	*httptest.Server
	mu     sync.Mutex
	team   map[string]string
	member map[string]string
}

func newPromptStub(t *testing.T) *promptStub {
	t.Helper()
	stub := &promptStub{team: map[string]string{}, member: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/teams/{team}/prompt", func(w http.ResponseWriter, r *http.Request) {
		team := r.PathValue("team")
		prompt, ok := stub.lookup(stub.team, team)
		if !ok {
			stub.fail(w, "Team '"+team+"' not found")
			return
		}
		stub.prompt(w, prompt)
	})
	mux.HandleFunc("GET /v1/teams/{team}/members/{name}/prompt", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		key := memberKey(r.PathValue("team"), name, r.URL.Query().Get("user"))
		prompt, ok := stub.lookup(stub.member, key)
		if !ok {
			stub.fail(w, "Member '"+name+"' not found")
			return
		}
		stub.prompt(w, prompt)
	})
	stub.Server = httptest.NewServer(mux)
	t.Cleanup(stub.Close)
	return stub
}

func memberKey(team, name, user string) string { return team + "\x00" + name + "\x00" + user }

func (s *promptStub) setTeam(team, prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.team[team] = prompt
}

func (s *promptStub) setMember(team, name, user, prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.member[memberKey(team, name, user)] = prompt
}

func (s *promptStub) lookup(m map[string]string, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt, ok := m[key]
	return prompt, ok
}

func (s *promptStub) prompt(w http.ResponseWriter, prompt string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"prompt": prompt})
}

func (s *promptStub) fail(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// promptBoard serves the board API in front of stub.
func promptBoard(t *testing.T, stub *promptStub) *httptest.Server {
	t.Helper()
	proxy := client.NewHTTP(stub.URL, &http.Client{Timeout: 5 * time.Second})
	srv := httptest.NewServer(newHandler(proxy, stubResolver(pane{}, nil)))
	t.Cleanup(srv.Close)
	return srv
}

func getPrompt(t *testing.T, base, path string) (int, map[string]string) {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp.StatusCode, out
}

func TestPromptProxyReturnsStoredPrompts(t *testing.T) {
	stub := newPromptStub(t)
	const want = "Repo is at /src\nRun make test"
	stub.setTeam("demo", want)
	srv := promptBoard(t, stub)

	code, body := getPrompt(t, srv.URL, "/api/teams/demo/prompt")
	if code != http.StatusOK || body["prompt"] != want {
		t.Fatalf("team prompt = %d %q", code, body["prompt"])
	}
}

func TestPromptProxyReportsMissingTeam(t *testing.T) {
	srv := promptBoard(t, newPromptStub(t))

	code, body := getPrompt(t, srv.URL, "/api/teams/nope/prompt")
	if code != http.StatusNotFound || !strings.Contains(body["error"], "nope") {
		t.Fatalf("missing team = %d %q", code, body["error"])
	}
}

func TestAgentPromptProxySeparatesSameNameUsers(t *testing.T) {
	stub := newPromptStub(t)
	stub.setMember("demo", "alice", "one", "Alice as one")
	stub.setMember("demo", "alice", "two", "Alice as two")
	srv := promptBoard(t, stub)

	for user, want := range map[string]string{"one": "Alice as one", "two": "Alice as two"} {
		path := "/api/teams/demo/members/alice/prompt?" + url.Values{"user": {user}}.Encode()
		code, body := getPrompt(t, srv.URL, path)
		if code != http.StatusOK || body["prompt"] != want {
			t.Fatalf("user %q = %d %q", user, code, body["prompt"])
		}
	}

	// The empty user is a distinct member key, so it must not fall back to one of them.
	code, body := getPrompt(t, srv.URL, "/api/teams/demo/members/alice/prompt?user=")
	if code != http.StatusNotFound || !strings.Contains(body["error"], "alice") {
		t.Fatalf("unscoped member = %d %q", code, body["error"])
	}
}

func TestAgentPromptProxyDecodesEscapedNames(t *testing.T) {
	stub := newPromptStub(t)
	team, name, user := "team one", "alice jr", "user two"
	const want = "Instructions for a name with spaces"
	stub.setMember(team, name, user, want)
	srv := promptBoard(t, stub)

	path := "/api/teams/" + url.PathEscape(team) + "/members/" + url.PathEscape(name) + "/prompt?" + url.Values{"user": {user}}.Encode()
	code, body := getPrompt(t, srv.URL, path)
	if code != http.StatusOK || body["prompt"] != want {
		t.Fatalf("escaped names = %d %q", code, body["prompt"])
	}
}

func TestAgentPromptProxyReportsMissingMember(t *testing.T) {
	srv := promptBoard(t, newPromptStub(t))

	code, body := getPrompt(t, srv.URL, "/api/teams/demo/members/bob/prompt?user=")
	if code != http.StatusNotFound || !strings.Contains(body["error"], "bob") {
		t.Fatalf("missing member = %d %q", code, body["error"])
	}
}
