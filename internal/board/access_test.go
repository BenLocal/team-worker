package board

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoardAccessProtectsAllRoutesAndSupportsLogin(t *testing.T) {
	protected, err := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "protected") }), "test-only-key", "/team-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/teams", "/api/service-keys", "/api/links", "/api/terminal/ws", "/assets/main.js"} {
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("unprotected %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Access key") {
		t.Fatal("missing sign-in page")
	}
	// no-referrer makes browsers post "Origin: null", which fails the
	// cross-origin check when the board is served over plain HTTP.
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want same-origin", got)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"key": {"wrong"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	protected.ServeHTTP(w, r)
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("wrong key accepted")
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"key": {"test-only-key"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	protected.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/team-worker/" {
		t.Fatalf("login redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Path != "/team-worker/" || cookie.SameSite != http.SameSiteStrictMode || strings.Contains(cookie.Value, "test-only-key") {
		t.Fatal("unsafe session cookie")
	}
	r = httptest.NewRequest("GET", "/api/teams", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "protected" {
		t.Fatal("session did not authenticate")
	}
	cookie.Value += "tampered"
	r = httptest.NewRequest("GET", "/api/teams", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("tampered cookie accepted")
	}
	r = httptest.NewRequest("POST", "/login", strings.NewReader("key=test-only-key"))
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, httptest.NewRequest("GET", "/?k=test-only-key&view=terminals", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/team-worker/?view=terminals" {
		t.Fatal("bootstrap key remained in URL")
	}
}

func TestServiceKeysAreAuthenticatedAndNeverExposeUnsealKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, entry := range []struct{ path, contents string }{
		{".config/magpie/web.key", "test-magpie-key"},
		{".openbao/init.json", `{"root_token":"test-root-token","keys_base64":["never-expose-unseal-key"]}`},
	} {
		file := filepath.Join(home, entry.path)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(entry.contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Protect(http.HandlerFunc(serviceKeys), "test-only-key", "")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/service-keys", nil))
	if w.Code != 401 || strings.Contains(w.Body.String(), "test-root-token") {
		t.Fatal("keys exposed without authentication")
	}
	r := httptest.NewRequest("GET", "/api/service-keys", nil)
	r.Header.Set("Authorization", "Bearer test-only-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "test-root-token") || !strings.Contains(w.Body.String(), "test-magpie-key") || strings.Contains(w.Body.String(), "never-expose") {
		t.Fatal("unexpected service key response")
	}
}
