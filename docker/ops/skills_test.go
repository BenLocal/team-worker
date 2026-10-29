package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeLibrary is a Magpie Web server: GET /?k= signs in with a cookie, and
// each API route answers from a queue of canned JSON responses.
type fakeLibrary struct {
	t         *testing.T
	server    *httptest.Server
	mu        sync.Mutex
	responses map[string][]string
	posts     []string
	bodies    map[string]any
	requests  int
	key       string
}

func newFakeLibrary(t *testing.T) *fakeLibrary {
	t.Helper()
	f := &fakeLibrary{t: t, responses: map[string][]string{}, bodies: map[string]any{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// respond queues responses for "METHOD /path"; the last one repeats.
func (f *fakeLibrary) respond(route string, bodies ...string) {
	f.responses[route] = append(f.responses[route], bodies...)
}

func (f *fakeLibrary) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		if k := r.URL.Query().Get("k"); k != "" {
			f.key = k
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		return
	}
	if r.Method == http.MethodPost {
		f.posts = append(f.posts, r.URL.Path)
		var body any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		f.bodies[r.URL.Path] = body
		if c, err := r.Cookie("session"); err != nil || c.Value != "test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	route := r.Method + " " + r.URL.Path
	queue := f.responses[route]
	if len(queue) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body := queue[0]
	if len(queue) > 1 {
		f.responses[route] = queue[1:]
	}
	if status, rest, ok := strings.Cut(body, "|"); ok && status == "500" {
		w.WriteHeader(http.StatusInternalServerError)
		body = rest
	}
	io.WriteString(w, body)
}

func (f *fakeLibrary) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepositoryAliasesAndRefs(t *testing.T) {
	if repository("https://code.example/group/repo.git") != repository("git@code.example:group/repo.git") {
		t.Error("HTTPS and SSH forms of one repository differ")
	}
	if repository("https://github.com/owner/repo/tree/main/skills") != repository("owner/repo") {
		t.Error("GitHub tree URL and owner/repo differ")
	}
	if got := repository("https://user:token@Git.Example/Team/Repo.git/"); got != "git.example/team/repo" {
		t.Errorf("credentials or case kept: %q", got)
	}
}

func TestSkillsUpdateLeavesSameNamedSkillFromAnotherSourceAlone(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.respond("GET /api/library",
		`{"agents": [{"id": "claude"}]}`,
		`{"skills": [{"name": "shared", "kind": "git", "repo": "git.example/another/repo"}]}`)
	lib.respond("POST /api/library/skills/probe",
		`{"kind": "git", "candidates": [{"name": "shared", "path": "skills/shared", "have": true}]}`)
	lib.respond("POST /api/library/all/sync", `{"result": {}}`)
	t.Setenv("MAGPIE_WEB_KEY", "test")
	path := writeFile(t, t.TempDir(), "skills.yaml",
		"sources: [{source: 'https://git.example/team/wanted.git', agents: [claude]}]\n")
	if err := configureSkills(lib.server.URL, path, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/library/skills/probe", "/api/library/all/sync"}
	if strings.Join(lib.posts, ",") != strings.Join(want, ",") {
		t.Errorf("posts = %v, want %v", lib.posts, want)
	}
}

func TestLibraryChangeReportsLocalEditsAsFailure(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.respond("POST /api/library/skills/update-some", `{"result": {"unupdated": [{"what": "skill:changed"}]}}`)
	client, err := newLibrary(lib.server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Change("skills/update-some", map[string]any{"names": []string{"changed"}})
	if err == nil || !strings.Contains(err.Error(), "local edits") {
		t.Fatalf("err = %v, want a local edits failure", err)
	}
}

func TestSkillsValidatesAllSourcesBeforeUsingLibrary(t *testing.T) {
	t.Setenv("MAGPIE_WEB_KEY", "test")
	for _, value := range []string{
		`null`,
		`{"sources": "wrong"}`,
		`{"sources": [{"source": "", "agents": ["claude"]}]}`,
		`{"sources": [{"source": "/skills", "agents": []}]}`,
		`{"sources": [{"source": "/skills", "agents": "claude"}]}`,
		`{"sources": [{"source": "/skills", "agents": [1]}]}`,
		`{"sources": [{"source": "/skills", "agents": ["claude"], "typo": true}]}`,
	} {
		lib := newFakeLibrary(t)
		path := writeFile(t, t.TempDir(), "skills.yaml", value)
		if err := configureSkills(lib.server.URL, path, false, io.Discard); err == nil {
			t.Errorf("%s: accepted", value)
		}
		if n := lib.requestCount(); n != 0 {
			t.Errorf("%s: %d requests before validation finished", value, n)
		}
	}
}

func TestSkillsEmptySourcesNeedNoKeyOrNetwork(t *testing.T) {
	lib := newFakeLibrary(t)
	t.Setenv("MAGPIE_WEB_KEY", "")
	t.Setenv("HOME", t.TempDir())
	path := writeFile(t, t.TempDir(), "skills.yaml", "sources: []\n")
	if err := configureSkills(lib.server.URL, path, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := lib.requestCount(); n != 0 {
		t.Errorf("%d requests for an empty config", n)
	}
}

func TestSkillsUnknownAgentRejectedBeforeProbeOrInstall(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.respond("GET /api/library", `{"agents": [{"id": "claude"}]}`)
	t.Setenv("MAGPIE_WEB_KEY", "test")
	path := writeFile(t, t.TempDir(), "skills.yaml", "sources: [{source: /skills, agents: [cluade]}]\n")
	err := configureSkills(lib.server.URL, path, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("err = %v, want an unknown agent error", err)
	}
	if len(lib.posts) != 0 {
		t.Errorf("posts = %v, want none", lib.posts)
	}
}

func TestSkillsCookieAuthProxyBypassAndFailedInstallNotRetried(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.respond("GET /api/library", `{"agents": [{"id": "claude"}]}`)
	lib.respond("POST /api/library/skills/probe", `{"candidates": [{"path": "one", "have": false}]}`)
	lib.respond("POST /api/library/skills/install", `500|private-repository-credential`)
	t.Setenv("MAGPIE_WEB_KEY", "key with / symbols")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	path := writeFile(t, t.TempDir(), "skills.yaml", "sources: [{source: /skills, agents: [claude]}]")
	err := configureSkills(lib.server.URL, path, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v, want HTTP 500", err)
	}
	if strings.Contains(err.Error(), "credential") {
		t.Errorf("error leaks the response body: %v", err)
	}
	want := []string{"/api/library/skills/probe", "/api/library/skills/install"}
	if strings.Join(lib.posts, ",") != strings.Join(want, ",") {
		t.Errorf("posts = %v, want %v", lib.posts, want)
	}
	if lib.key != "key with / symbols" {
		t.Errorf("signed in with %q", lib.key)
	}
}
