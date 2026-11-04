package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type baoCall struct {
	method, path string
	payload      any
}

// fakeBao imitates the OpenBao endpoints the importer uses.
type fakeBao struct {
	mu          sync.Mutex
	calls       []baoCall
	tokens      []string
	mounts      map[string]any
	auth        map[string]any
	stored      map[string]map[string]any
	initialized bool
	sealed      bool
	// Logins with these secret IDs fail; loginsFail fails every login until
	// the AppRole is deleted.
	badSecrets map[string]bool
	loginsFail bool
	secretSeq  int
}

func newFakeBao(t *testing.T) (*fakeBao, *baoClient) {
	t.Helper()
	f := &fakeBao{mounts: map[string]any{}, auth: map[string]any{}, stored: map[string]map[string]any{}, initialized: true}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old, oldPoll := openbaoAddress, openbaoPoll
	openbaoAddress, openbaoPoll = srv.URL, time.Millisecond
	t.Cleanup(func() { openbaoAddress, openbaoPoll = old, oldPoll })
	return f, newBaoClient(srv.URL)
}

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	var payload any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		json.Unmarshal(raw, &payload)
	}
	f.calls = append(f.calls, baoCall{r.Method, path, payload})
	f.tokens = append(f.tokens, r.Header.Get("X-Vault-Token"))
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case path == "sys/health":
		reply(map[string]any{})
	case path == "sys/mounts" && r.Method == "GET":
		reply(map[string]any{"data": f.mounts})
	case path == "sys/auth" && r.Method == "GET":
		reply(map[string]any{"data": f.auth})
	case r.Method == "GET" && strings.HasPrefix(path, "secret/data/"):
		data, ok := f.stored[strings.TrimPrefix(path, "secret/data/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(map[string]any{"data": map[string]any{"data": data}})
	case strings.HasSuffix(path, "/role-id"):
		reply(map[string]any{"data": map[string]any{"role_id": "role-123"}})
	case strings.HasSuffix(path, "/secret-id"):
		id := "secret-456"
		if f.secretSeq > 0 {
			id = fmt.Sprintf("secret-%d", 456+f.secretSeq)
		}
		f.secretSeq++
		reply(map[string]any{"data": map[string]any{"secret_id": id}})
	case path == "auth/approle/login":
		body, _ := payload.(map[string]any)
		if f.loginsFail || f.badSecrets[fmt.Sprint(body["secret_id"])] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		reply(map[string]any{"auth": map[string]any{"client_token": "agent-token"}})
	case path == "auth/approle/role/team-worker-agent" && r.Method == "DELETE":
		f.loginsFail = false
		reply(map[string]any{})
	case path == "sys/seal-status":
		reply(map[string]any{"initialized": f.initialized, "sealed": f.sealed, "t": 1})
	case path == "sys/init":
		f.initialized, f.sealed = true, true
		reply(map[string]any{"keys_base64": []string{"k1"}, "root_token": "root-1", "keys": []string{"hex"}})
	case path == "sys/unseal":
		f.sealed = false
		reply(map[string]any{})
	default:
		reply(map[string]any{})
	}
}

func (f *fakeBao) writes() []baoCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []baoCall
	for _, c := range f.calls {
		if c.method != "GET" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeBao) paths(calls []baoCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.path)
	}
	return out
}

// jsonValue round-trips v so it compares equal to a decoded request body.
func jsonValue(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	json.Unmarshal(raw, &out)
	return out
}

func hasCall(calls []baoCall, method, path string, payload any) bool {
	for _, c := range calls {
		if c.method == method && c.path == path && reflect.DeepEqual(c.payload, jsonValue(payload)) {
			return true
		}
	}
	return false
}

func writeBaoConfig(t *testing.T, dir string, config any) string {
	t.Helper()
	path := filepath.Join(dir, "openbao.yaml")
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenBaoConfigAcceptsSecretsAndFiles(t *testing.T) {
	path := writeBaoConfig(t, t.TempDir(), map[string]any{
		"secrets": map[string]any{"team/env": map[string]any{"TOKEN": "v"}},
		"files":   []any{map[string]any{"path": "/home/node/x.env", "template": "t"}},
	})
	secrets, files, err := readOpenBaoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets, map[string]map[string]string{"team/env": {"TOKEN": "v"}}) {
		t.Errorf("secrets = %v", secrets)
	}
	if !reflect.DeepEqual(files, []openbaoFile{{Path: "/home/node/x.env", Template: "t"}}) {
		t.Errorf("files = %v", files)
	}
}

func TestOpenBaoConfigRejectsInvalidEntries(t *testing.T) {
	cases := []any{
		map[string]any{"other": map[string]any{}},
		map[string]any{"secrets": map[string]any{"../escape": map[string]any{"k": "v"}}},
		map[string]any{"secrets": map[string]any{"team/./x": map[string]any{"k": "v"}}},
		map[string]any{"secrets": map[string]any{"team/env": map[string]any{}}},
		map[string]any{"secrets": map[string]any{"team/env": map[string]any{"k": 1}}},
		map[string]any{"files": []any{map[string]any{"path": "relative.env", "template": "t"}}},
		map[string]any{"files": []any{map[string]any{"path": "/a", "template": " "}}},
		map[string]any{"files": []any{map[string]any{"path": "/a", "template": "t", "mode": "0644"}}},
		map[string]any{"files": []any{
			map[string]any{"path": "/a", "template": "t"},
			map[string]any{"path": "/a", "template": "u"},
		}},
	}
	for _, config := range cases {
		if _, _, err := readOpenBaoConfig(writeBaoConfig(t, t.TempDir(), config)); err == nil {
			t.Errorf("config %v was accepted", config)
		}
	}
}

func TestImportCreatesMountWritesChangedSecretsAndReadOnlyPolicy(t *testing.T) {
	f, c := newFakeBao(t)
	f.stored["team/same"] = map[string]any{"k": "v"}
	err := importSecrets(c, map[string]map[string]string{"team/same": {"k": "v"}, "team/new": {"TOKEN": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	writes := f.writes()
	if !hasCall(writes, "POST", "sys/mounts/secret", map[string]any{"type": "kv", "options": map[string]any{"version": "2"}}) {
		t.Error("KV v2 mount not created")
	}
	if !hasCall(writes, "POST", "secret/data/team/new", map[string]any{"data": map[string]any{"TOKEN": "x"}}) {
		t.Error("new secret not written")
	}
	for _, p := range f.paths(writes) {
		if p == "secret/data/team/same" {
			t.Error("unchanged secret was rewritten")
		}
	}
	var policy string
	for _, w := range writes {
		if w.path == "sys/policies/acl/team-worker-agent" {
			policy, _ = w.payload.(map[string]any)["policy"].(string)
		}
	}
	if !strings.Contains(policy, `path "secret/data/team/new" { capabilities = ["read"] }`) || strings.Contains(policy, "update") {
		t.Errorf("policy = %q", policy)
	}
	if !hasCall(writes, "POST", "sys/auth/approle", map[string]any{"type": "approle"}) {
		t.Error("AppRole auth not enabled")
	}
}

func TestImportReusesExistingMountAndAppRole(t *testing.T) {
	f, c := newFakeBao(t)
	f.mounts["secret/"] = map[string]any{}
	f.auth["approle/"] = map[string]any{}
	if err := importSecrets(c, map[string]map[string]string{"team/env": {"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.paths(f.writes()) {
		if p == "sys/mounts/secret" || p == "sys/auth/approle" {
			t.Errorf("unexpected write to %s", p)
		}
	}
}

func TestConfigureWaitsImportsAndWritesPrivateAgentConfig(t *testing.T) {
	f, _ := newFakeBao(t)
	root := t.TempDir()
	config := writeBaoConfig(t, root, map[string]any{
		"secrets": map[string]any{"team/env": map[string]any{"TOKEN": "x"}},
		"files":   []any{map[string]any{"path": filepath.Join(root, "out.env"), "template": "{{ . }}"}},
	})
	token := filepath.Join(root, "token")
	os.WriteFile(token, []byte("admin-token\n"), 0o600)

	path, err := configureOpenBao(config, token, root, false)
	if err != nil {
		t.Fatal(err)
	}
	// The import uses the import token; only the Agent's own login check does not.
	for i, c := range f.calls {
		if c.path == "sys/policies/acl/team-worker-agent" && f.tokens[i] != "admin-token" {
			t.Errorf("import token = %q", f.tokens[i])
		}
	}
	state := filepath.Join(root, ".openbao/agent")
	for name, want := range map[string]string{"role_id": "role-123", "secret_id": "secret-456"} {
		if got, _ := os.ReadFile(filepath.Join(state, name)); string(got) != want {
			t.Errorf("%s = %q", name, got)
		}
	}
	for _, name := range []string{"role_id", "secret_id", "agent.json"} {
		info, err := os.Stat(filepath.Join(state, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, %v", name, info.Mode().Perm(), err)
		}
	}
	var agent struct {
		Template []map[string]any `json:"template"`
		AutoAuth struct {
			Method []map[string]any `json:"method"`
		} `json:"auto_auth"`
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Template[0]["destination"] != filepath.Join(root, "out.env") || agent.Template[0]["perms"] != "0600" {
		t.Errorf("template = %v", agent.Template[0])
	}
	if agent.AutoAuth.Method[0]["type"] != "approle" {
		t.Errorf("auth method = %v", agent.AutoAuth.Method[0])
	}

	// A second start keeps the existing secret ID instead of minting more.
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	if _, err := configureOpenBao(config, token, root, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.paths(f.calls) {
		if p == "auth/approle/role/team-worker-agent/secret-id" {
			t.Error("a second secret ID was minted")
		}
	}
}

func TestConfigureEmptyConfigDoesNotContactOpenBao(t *testing.T) {
	f, _ := newFakeBao(t)
	dir := t.TempDir()
	config := writeBaoConfig(t, dir, map[string]any{"secrets": map[string]any{}, "files": []any{}})
	path, err := configureOpenBao(config, filepath.Join(dir, "t"), dir, false)
	if err != nil || path != "" {
		t.Fatalf("path = %q, err = %v", path, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("OpenBao was called: %v", f.calls)
	}
}

func TestAutoUnsealInitializesOnceThenUnsealsWithStoredKey(t *testing.T) {
	f, c := newFakeBao(t)
	f.initialized, f.sealed = false, true
	initFile := filepath.Join(t.TempDir(), "init.json")

	if got := autoUnsealState(c, initFile); got != "just initialized" {
		t.Fatalf("state = %q", got)
	}
	info, _ := os.Stat(initFile)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("init.json mode = %v", info.Mode().Perm())
	}
	var stored map[string]any
	raw, _ := os.ReadFile(initFile)
	json.Unmarshal(raw, &stored)
	if !reflect.DeepEqual(stored, map[string]any{"keys_base64": []any{"k1"}, "root_token": "root-1"}) {
		t.Errorf("stored = %v", stored)
	}
	if got := autoUnsealState(c, initFile); got != "unsealing" {
		t.Fatalf("state = %q", got)
	}
	if !hasCall(f.calls, "PUT", "sys/unseal", map[string]any{"key": "k1"}) {
		t.Error("stored key not used to unseal")
	}
	if got := autoUnsealState(c, initFile); got != "ready" {
		t.Fatalf("state = %q", got)
	}
	var inits []baoCall
	for _, call := range f.calls {
		if call.path == "sys/init" {
			inits = append(inits, call)
		}
	}
	if len(inits) != 1 || !hasCall(inits, "PUT", "sys/init", map[string]any{"secret_shares": 1, "secret_threshold": 1}) {
		t.Errorf("init calls = %v", inits)
	}
}

func TestAutoUnsealSealedWithoutStoredKeysWaitsForAPerson(t *testing.T) {
	f, c := newFakeBao(t)
	f.sealed = true
	state := autoUnsealState(c, filepath.Join(t.TempDir(), "init.json"))
	if !strings.Contains(state, "unseal it manually") {
		t.Errorf("state = %q", state)
	}
	for _, p := range f.paths(f.calls) {
		if p == "sys/unseal" {
			t.Error("unseal attempted without keys")
		}
	}
}

func TestAutoUnsealReinitializationKeepsPreviousKeys(t *testing.T) {
	f, c := newFakeBao(t)
	f.initialized, f.sealed = false, true
	dir := t.TempDir()
	initFile := filepath.Join(dir, "init.json")
	os.WriteFile(initFile, []byte(`{"keys_base64": ["old"], "root_token": "old"}`), 0o600)
	autoUnsealState(c, initFile)
	backups, _ := filepath.Glob(filepath.Join(dir, "init.json.bak-*"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if raw, _ := os.ReadFile(backups[0]); !strings.Contains(string(raw), "old") {
		t.Errorf("backup = %s", raw)
	}
}

func TestAutoUnsealWaitsForUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if got := autoUnsealState(newBaoClient(srv.URL), filepath.Join(t.TempDir(), "init.json")); got != "not started" {
		t.Errorf("state = %q", got)
	}
}

func TestWaitForRenderedFiles(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".openbao/agent")
	os.MkdirAll(state, 0o700)
	target := filepath.Join(dir, "git-credentials")
	config := filepath.Join(dir, "openbao.yaml")
	os.WriteFile(config, []byte(`{"secrets": {"app/git": {"password": "x"}},
"files": [{"path": "`+target+`", "template": "t"}]}`), 0o600)
	if err := waitForRenderedFiles(config, dir, 600*time.Millisecond); err == nil {
		t.Fatal("missing file should time out")
	}
	// A file an earlier start left is not this start's render.
	os.WriteFile(target, []byte("https://u:p@host\n"), 0o600)
	os.WriteFile(filepath.Join(state, "rendered"), []byte("old"), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(state, "rendered"), old, old)
	os.WriteFile(filepath.Join(state, "started"), []byte("now"), 0o600)
	if err := waitForRenderedFiles(config, dir, 600*time.Millisecond); err == nil || !strings.Contains(err.Error(), "earlier start") {
		t.Fatalf("stale files must not count: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(filepath.Join(state, "rendered"), []byte("now"), 0o600)
	}()
	if err := waitForRenderedFiles(config, dir, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigRendersAStartMarker(t *testing.T) {
	cfg := agentConfig("/h", []openbaoFile{{Path: "/h/out.env", Template: "x"}})
	templates := cfg["template"].([]map[string]any)
	last := templates[len(templates)-1]
	if len(templates) != 2 || last["destination"] != "/h/.openbao/agent/rendered" || last["contents"] != "{{ timestamp }}" {
		t.Fatalf("templates = %v", templates)
	}
}

func agentState(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	config := writeBaoConfig(t, root, map[string]any{
		"secrets": map[string]any{"team/env": map[string]any{"TOKEN": "x"}},
		"files":   []any{map[string]any{"path": filepath.Join(root, "out.env"), "template": "{{ . }}"}},
	})
	token := filepath.Join(root, "token")
	os.WriteFile(token, []byte("admin-token\n"), 0o600)
	return root, config
}

func TestConfigureReplacesASecretIDThatNoLongerLogsIn(t *testing.T) {
	f, _ := newFakeBao(t)
	root, config := agentState(t)
	state := filepath.Join(root, ".openbao/agent")
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "secret_id"), []byte("from-clawteam"), 0o600)
	f.badSecrets = map[string]bool{"from-clawteam": true}
	if _, err := configureOpenBao(config, filepath.Join(root, "token"), root, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(state, "secret_id")); string(got) != "secret-456" {
		t.Fatalf("secret_id = %q", got)
	}
	if _, err := os.Stat(filepath.Join(state, "started")); err != nil {
		t.Fatal("the agent's start must be recorded")
	}
	// The check logs in without the import token.
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c.path == "auth/approle/login" && f.tokens[i] != "" {
			t.Fatalf("login sent a token: %q", f.tokens[i])
		}
	}
}

func TestConfigureRecreatesAnAppRoleThatRefusesEveryLogin(t *testing.T) {
	f, _ := newFakeBao(t)
	root, config := agentState(t)
	f.loginsFail = true
	if _, err := configureOpenBao(config, filepath.Join(root, "token"), root, false); err != nil {
		t.Fatal(err)
	}
	if !hasCall(f.writes(), "DELETE", "auth/approle/role/team-worker-agent", nil) {
		t.Fatal("the AppRole was not recreated")
	}
	if got, _ := os.ReadFile(filepath.Join(root, ".openbao/agent/secret_id")); string(got) != "secret-457" {
		t.Fatalf("secret_id = %q, want the one issued after recreating", got)
	}
}

func TestPrepareRaftRecoversDataFromAnotherNodeOnce(t *testing.T) {
	data := t.TempDir()
	if wrote, err := prepareRaft(data, "team-worker", "127.0.0.1:8201"); err != nil || wrote {
		t.Fatalf("new data needs nothing: %v %v", wrote, err)
	}
	os.MkdirAll(filepath.Join(data, "raft"), 0o700)
	os.WriteFile(filepath.Join(data, "raft", "raft.db"), []byte("x"), 0o600)
	if wrote, err := prepareRaft(data, "team-worker", "127.0.0.1:8201"); err != nil || !wrote {
		t.Fatalf("existing data must be recovered: %v %v", wrote, err)
	}
	var peers []map[string]any
	raw, _ := os.ReadFile(filepath.Join(data, "raft", "peers.json"))
	if err := json.Unmarshal(raw, &peers); err != nil || len(peers) != 1 || peers[0]["id"] != "team-worker" ||
		peers[0]["address"] != "127.0.0.1:8201" || peers[0]["non_voter"] != false {
		t.Fatalf("peers.json = %s, %v", raw, err)
	}
	os.Remove(filepath.Join(data, "raft", "peers.json")) // raft consumes it at start
	if wrote, err := prepareRaft(data, "team-worker", "127.0.0.1:8201"); err != nil || wrote {
		t.Fatalf("a prepared directory is left alone: %v %v", wrote, err)
	}
}
