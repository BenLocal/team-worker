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
	// Path / lets the proxy's checks for Magpie and OpenBao see the session.
	if !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode || strings.Contains(cookie.Value, "test-only-key") {
		t.Fatal("unsafe session cookie")
	}
	r = httptest.NewRequest("GET", "/api/teams", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "protected" {
		t.Fatal("session did not authenticate")
	}
	// An older session cookie scoped to the base path is sent first; a
	// stale one there must not hide the current one.
	r = httptest.NewRequest("GET", "/api/teams", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "1.stale.signature"})
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("a stale cookie beside the session blocked it")
	}
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, httptest.NewRequest("POST", "/logout", nil))
	cleared := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared[c.Path] = true
		}
	}
	if !cleared["/"] || !cleared["/team-worker/"] {
		t.Fatalf("logout cleared %v, want / and /team-worker/", cleared)
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

func TestOpeningTheBoardMovesAnOldSessionToTheRootPathAndFollowsNext(t *testing.T) {
	protected, err := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "protected") }), "test-only-key", "/team-worker")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, httptest.NewRequest("GET", "/?k=test-only-key", nil))
	session := w.Result().Cookies()[0]
	// The browser sends the cookie it kept from before, wherever it was scoped.
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Value})
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, r)
	moved, cleared := false, false
	for _, c := range w.Result().Cookies() {
		switch {
		case c.Path == "/" && c.Value == session.Value && c.MaxAge > 0 && c.HttpOnly:
			moved = true
		case c.Path == "/team-worker/" && c.MaxAge < 0:
			cleared = true
		}
	}
	if w.Code != 200 || !moved || !cleared {
		t.Fatalf("session not moved to /: %d moved=%v cleared=%v", w.Code, moved, cleared)
	}
	for next, want := range map[string]string{"/magpie/": "/magpie/", "//other.example/": "", "https://other.example/": ""} {
		r = httptest.NewRequest("GET", "/?next="+url.QueryEscape(next), nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session.Value})
		w = httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		if got := w.Header().Get("Location"); got != want || (want == "" && w.Code != 200) {
			t.Fatalf("next=%s: %d %q, want %q", next, w.Code, got, want)
		}
	}
	// Signing in keeps next, so the board then goes back to it.
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, httptest.NewRequest("GET", "/?k=test-only-key&next=%2Fmagpie%2F", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/team-worker/?next=%2Fmagpie%2F" {
		t.Fatalf("sign-in dropped next: %d %s", w.Code, w.Header().Get("Location"))
	}
}
