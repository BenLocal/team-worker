package board

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "team_worker_session"

// Protect requires the shared API key for pages, APIs and WebSocket upgrades.
// basePath is the externally visible prefix when a reverse proxy strips it.
func Protect(next http.Handler, key, basePath string) (http.Handler, error) {
	if basePath == "/" {
		basePath = ""
	}
	if key == "" {
		return nil, fmt.Errorf("Web access key must not be empty")
	}
	if basePath != "" && (path.Clean(basePath) != basePath || !strings.HasPrefix(basePath, "/") || strings.HasPrefix(basePath, "//") || strings.ContainsAny(basePath, "?#\\\r\n")) {
		return nil, fmt.Errorf("Web base path must be a clean absolute path without a trailing slash")
	}
	expected := sha256.Sum256([]byte(key))
	matches := func(value string) bool {
		actual := sha256.Sum256([]byte(value))
		return hmac.Equal(actual[:], expected[:])
	}
	sign := func(value string) string {
		h := hmac.New(sha256.New, []byte(key))
		h.Write([]byte(value))
		return hex.EncodeToString(h.Sum(nil))
	}
	// validSession returns the session cookie's value and expiry. A browser
	// can hold an older session scoped to basePath beside the current one, so
	// any valid cookie of that name will do.
	validSession := func(r *http.Request) (string, time.Time, bool) {
		for _, cookie := range r.CookiesNamed(sessionCookie) {
			parts := strings.Split(cookie.Value, ".")
			if len(parts) != 3 {
				continue
			}
			expires, err := strconv.ParseInt(parts[0], 10, 64)
			if err == nil && expires > time.Now().Unix() && hmac.Equal([]byte(parts[2]), []byte(sign(parts[0]+"."+parts[1]))) {
				return cookie.Value, time.Unix(expires, 0), true
			}
		}
		return "", time.Time{}, false
	}
	// writeSession sets the cookie on Path / so the proxy's auth_request for
	// Magpie and the OpenBao sign-in, outside basePath, see the session too.
	writeSession := func(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	}
	setSession := func(w http.ResponseWriter, r *http.Request) bool {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			http.Error(w, "Unable to create session", 500)
			return false
		}
		expires := time.Now().Add(12 * time.Hour)
		value := strconv.FormatInt(expires.Unix(), 10) + "." + hex.EncodeToString(nonce[:])
		writeSession(w, r, value+"."+sign(value), expires)
		return true
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// same-origin keeps a ?k= URL out of other sites' Referer. no-referrer
		// would also make browsers send "Origin: null" on same-origin POSTs,
		// which the cross-origin check rejects over plain HTTP.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/logout" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid sign-in request", 400)
				return
			}
			if !matches(r.PostForm.Get("key")) {
				loginPage(w, r, true)
				return
			}
			if setSession(w, r) {
				query := r.URL.Query()
				query.Del("k")
				target := basePath + "/"
				if len(query) > 0 {
					target += "?" + query.Encode()
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
			}
			return
		}
		if r.URL.Path == "/logout" && r.Method == http.MethodPost {
			// Earlier sessions were scoped to basePath; clear those as well.
			for _, p := range []string{"/", basePath + "/"} {
				http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: p, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
			}
			http.Redirect(w, r, basePath+"/", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/" && r.Method == http.MethodGet && r.URL.Query().Has("k") {
			if !matches(r.URL.Query().Get("k")) {
				loginPage(w, r, true)
				return
			}
			if setSession(w, r) {
				query := r.URL.Query()
				query.Del("k")
				target := basePath + "/"
				if len(query) > 0 {
					target += "?" + query.Encode()
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
			}
			return
		}
		bearer, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if hasBearer && matches(bearer) {
			next.ServeHTTP(w, r)
			return
		}
		if value, expires, ok := validSession(r); ok {
			if r.URL.Path == "/" && r.Method == http.MethodGet {
				// A session from before cookies moved to Path / only reaches
				// basePath; opening the board moves it, keeping its expiry.
				writeSession(w, r, value, expires)
				if basePath != "" {
					http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: basePath + "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
				}
				// The proxy sends a viewer without a session here with
				// ?next=<page>; go back there once signed in.
				if target := localPath(r.URL.Query().Get("next")); target != "" {
					http.Redirect(w, r, target, http.StatusSeeOther)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			loginPage(w, r, false)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":"Unauthorized"}`)
	})
	return http.NewCrossOriginProtection().Handler(handler), nil
}

// localPath returns p when it is a path on this site, so ?next= cannot send
// the viewer to another one.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return ""
	}
	return p
}

var signIn = template.Must(template.New("sign-in").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · team-worker</title><style>
:root{color-scheme:light dark;--bg:#f4f4f6;--card:#fff;--line:#e3e3e8;--fg:#1c1c21;--fg-2:#4b4b53;--muted:#85858d;--accent:#4f46e5;--accent-fg:#fff;--red:#d64545;--red-soft:#fbe9e9}
@media (prefers-color-scheme:dark){:root{--bg:#1a1a1e;--card:#232327;--line:#333339;--fg:#ededf1;--fg-2:#b9b9c1;--muted:#8b8b94;--accent:#a5b4fc;--accent-fg:#14142a;--red:#f28b82;--red-soft:#45272a}}
html{background:var(--bg);color:var(--fg);font:13px/1.45 -apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI",system-ui,"PingFang SC","Microsoft YaHei UI",sans-serif;-webkit-font-smoothing:antialiased}body{margin:0;min-height:100vh;display:grid;place-items:center}main{width:min(360px,calc(100vw - 32px));padding:28px;box-sizing:border-box;background:var(--card);border:1px solid var(--line);border-radius:14px}h1{font-size:20px;font-weight:600;letter-spacing:-.01em;margin:0 0 6px}p{margin:0;color:var(--fg-2)}label{display:block;margin:20px 0 6px;font-weight:500;color:var(--fg-2)}input,button{font:inherit;width:100%;box-sizing:border-box;border-radius:8px;height:34px;padding:0 11px}input{border:1px solid var(--line);background:var(--bg);color:var(--fg)}button{margin-top:14px;border:0;background:var(--accent);color:var(--accent-fg);font-weight:600;cursor:pointer}input:focus-visible,button:focus-visible{outline:2px solid var(--accent);outline-offset:1px}.error{margin-top:12px;padding:8px 10px;border-radius:8px;background:var(--red-soft);color:var(--red)}small{display:block;margin-top:18px;color:var(--muted);font-size:12px}</style></head><body><main><h1>team-worker</h1><p>Enter your access key to open the team board.</p>{{if .Error}}<p class="error" role="alert">The access key is incorrect.</p>{{end}}<form method="post" action="{{.Action}}"><label for="key">Access key</label><input id="key" name="key" type="password" autocomplete="current-password" required autofocus><button type="submit">Sign in</button></form><small>Use the same key as your team-worker CLI HTTP connection.</small></main></body></html>`))

func loginPage(w http.ResponseWriter, r *http.Request, invalid bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if invalid {
		w.WriteHeader(http.StatusUnauthorized)
	}
	query := r.URL.Query()
	query.Del("k")
	action := "login"
	if len(query) > 0 {
		action += "?" + query.Encode()
	}
	if err := signIn.Execute(w, struct {
		Error  bool
		Action string
	}{invalid, action}); err != nil {
		return
	}
}
