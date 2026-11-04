package board

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHomeFile(t *testing.T, home, path, contents string) {
	t.Helper()
	file := filepath.Join(home, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func signedIn(path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer test-only-key")
	return r
}

func TestMagpieAuthNeedsTheSessionAndReturnsTheKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	writeHomeFile(t, home, ".config/magpie/web.key", "test-magpie-key\n")
	h, err := Protect(newHandler(nil, nil), "test-only-key", "/team-worker")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedIn("/api/service-auth/magpie"))
	if w.Code != 404 || w.Header().Get("X-Magpie-Key") != "" {
		t.Fatalf("Magpie key returned while session sign-in is off: %d", w.Code)
	}
	t.Setenv("TEAM_WORKER_MAGPIE_SESSION_LOGIN", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/service-auth/magpie", nil))
	if w.Code != 401 || w.Header().Get("X-Magpie-Key") != "" {
		t.Fatalf("Magpie key without a session: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, signedIn("/api/service-auth/magpie"))
	if w.Code != 204 || w.Header().Get("X-Magpie-Key") != "test-magpie-key" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Magpie auth: %d %q", w.Code, w.Header().Get("X-Magpie-Key"))
	}
}

func TestOpenBaoLoginHandsTheUIAWrappedChildToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHomeFile(t, home, ".openbao/init.json", `{"root_token":"test-root-token","keys_base64":["never-expose-unseal-key"]}`)
	var got struct {
		token, wrapTTL string
		body           map[string]any
	}
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/auth/token/create" {
			http.NotFound(w, r)
			return
		}
		got.token, got.wrapTTL = r.Header.Get("X-Vault-Token"), r.Header.Get("X-Vault-Wrap-TTL")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &got.body)
		io.WriteString(w, `{"wrap_info":{"token":"test-wrapping-token"}}`)
	}))
	defer bao.Close()
	defer func(old string) { openbaoAddress = old }(openbaoAddress)
	openbaoAddress = bao.URL
	h, err := Protect(newHandler(nil, nil), "test-only-key", "/team-worker")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedIn("/api/openbao/login"))
	if w.Code != 404 || got.token != "" {
		t.Fatalf("token created while session sign-in is off: %d", w.Code)
	}
	t.Setenv("TEAM_WORKER_OPENBAO_SESSION_LOGIN", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/openbao/login", nil))
	if w.Code != 401 || got.token != "" {
		t.Fatalf("token created without a session: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, signedIn("/api/openbao/login"))
	location := w.Header().Get("Location")
	if w.Code != 303 || location != "/ui/vault/auth?with=token&wrapped_token=test-wrapping-token" {
		t.Fatalf("OpenBao login: %d %s", w.Code, location)
	}
	if got.token != "test-root-token" || got.wrapTTL != "60s" || got.body["ttl"] != "12h" {
		t.Fatalf("token request: %+v", got)
	}
	if strings.Contains(w.Body.String(), "test-root-token") || strings.Contains(location, "test-root-token") {
		t.Fatal("root token reached the browser")
	}
}

func TestOpenBaoLoginFallsBackToTheUIWithoutASavedRootToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TEAM_WORKER_OPENBAO_SESSION_LOGIN", "1")
	w := httptest.NewRecorder()
	openbaoLogin(w, httptest.NewRequest("GET", "/api/openbao/login", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/ui/" {
		t.Fatalf("manual setup: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestOpenBaoLoginReportsAFailedTokenRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHomeFile(t, home, ".openbao/init.json", `{"root_token":"test-root-token"}`)
	t.Setenv("TEAM_WORKER_OPENBAO_SESSION_LOGIN", "1")
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":["test-root-token is sealed"]}`, http.StatusServiceUnavailable)
	}))
	defer bao.Close()
	defer func(old string) { openbaoAddress = old }(openbaoAddress)
	openbaoAddress = bao.URL
	w := httptest.NewRecorder()
	openbaoLogin(w, httptest.NewRequest("GET", "/api/openbao/login", nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "503") || strings.Contains(w.Body.String(), "test-root-token") {
		t.Fatalf("failed request: %d %s", w.Code, w.Body.String())
	}
}
