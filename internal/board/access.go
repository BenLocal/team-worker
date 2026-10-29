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
	validSession := func(r *http.Request) bool {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			return false
		}
		parts := strings.Split(cookie.Value, ".")
		if len(parts) != 3 {
			return false
		}
		expires, err := strconv.ParseInt(parts[0], 10, 64)
		return err == nil && expires > time.Now().Unix() && hmac.Equal([]byte(parts[2]), []byte(sign(parts[0]+"."+parts[1])))
	}
	setSession := func(w http.ResponseWriter, r *http.Request) bool {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			http.Error(w, "Unable to create session", 500)
			return false
		}
		expires := time.Now().Add(12 * time.Hour)
		value := strconv.FormatInt(expires.Unix(), 10) + "." + hex.EncodeToString(nonce[:])
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value + "." + sign(value), Path: basePath + "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", Expires: expires, MaxAge: 12 * 60 * 60})
		return true
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
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
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: basePath + "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
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
		if (hasBearer && matches(bearer)) || validSession(r) {
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

var signIn = template.Must(template.New("sign-in").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · team-worker</title><style>
html{color-scheme:dark;background:#070a10;color:#e8e0cc;font:15px/1.6 ui-monospace,monospace}body{margin:0;min-height:100vh;display:grid;place-items:center}main{width:min(360px,calc(100vw - 48px));padding:32px}h1{font:italic 34px Georgia,serif;margin:0 0 12px}p{color:#c0b8a2}label{display:block;margin:24px 0 8px}input,button{font:inherit;width:100%;box-sizing:border-box;border:1px solid #4a5466;border-radius:5px;padding:11px 12px}input{background:#11161d;color:#e8e0cc}button{margin-top:16px;background:#e8c47e;color:#070a10;cursor:pointer}input:focus-visible,button:focus-visible{outline:2px solid #7ec7c2;outline-offset:3px}.error{color:#ff8864}small{display:block;margin-top:24px;color:#7a7466}</style></head><body><main><h1>team-worker</h1><p>Enter your access key to open the team board.</p>{{if .Error}}<p class="error" role="alert">The access key is incorrect.</p>{{end}}<form method="post" action="{{.Action}}"><label for="key">Access key</label><input id="key" name="key" type="password" autocomplete="current-password" required autofocus><button type="submit">Sign in</button></form><small>Use the same key as your team-worker CLI HTTP connection.</small></main></body></html>`))

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
