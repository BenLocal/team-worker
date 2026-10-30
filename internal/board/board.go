// Package board serves the web board for watching teams and their live
// agent terminals.
package board

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/proc"
)

// dist holds the Vite build of the page (`make web`, not committed); all:
// keeps files Vite names with a leading underscore.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the board page, a small JSON API backed by the daemon and
// the live terminal endpoints.
func Handler(c *client.Client) http.Handler {
	return newHandler(c, daemonResolver(c))
}

func newHandler(c *client.Client, resolve resolver) http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(dist, "dist")
	fileServer := http.FileServerFS(files)
	_, err := fs.Stat(files, "index.html")
	built := err == nil
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// The page is a build artifact; a binary built without `make web`
		// would otherwise serve a listing of the placeholder directory.
		if !built {
			http.Error(w, "The board page was not built into this binary; run `make web` and rebuild.", http.StatusServiceUnavailable)
			return
		}
		// Vite fingerprints everything under assets/; the page itself must
		// be revalidated so a new build is picked up.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
	terms := newTerminals(resolve)
	mux.HandleFunc("GET /api/service-keys", serviceKeys)
	mux.HandleFunc("POST /api/terminal/ticket", terms.handleTicket)
	mux.HandleFunc("GET /api/terminal/ws", terms.handleWS)
	mux.HandleFunc("GET /api/teams", func(w http.ResponseWriter, r *http.Request) {
		teams, err := c.ListTeams(r.Context())
		reply(w, teams, err)
	})
	mux.HandleFunc("GET /api/teams/{team}", func(w http.ResponseWriter, r *http.Request) {
		// The page shows the newest messages and pages back through the rest.
		b, err := c.Board(r.Context(), r.PathValue("team"), 500)
		reply(w, b, err)
	})
	// Prompts are read only when a viewer asks for one; the board snapshot
	// deliberately leaves them out so polling stays small.
	mux.HandleFunc("GET /api/teams/{team}/prompt", func(w http.ResponseWriter, r *http.Request) {
		prompt, err := c.TeamPrompt(r.Context(), r.PathValue("team"))
		reply(w, map[string]string{"prompt": prompt}, err)
	})
	mux.HandleFunc("GET /api/teams/{team}/members/{name}/prompt", func(w http.ResponseWriter, r *http.Request) {
		prompt, err := c.AgentPrompt(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user"))
		reply(w, map[string]string{"prompt": prompt}, err)
	})
	// Oversight is what waits on the leader or limits the team: plans, join
	// requests and spending. It is polled separately from the snapshot.
	mux.HandleFunc("GET /api/teams/{team}/oversight", func(w http.ResponseWriter, r *http.Request) {
		v, err := oversight(r.Context(), c, r.PathValue("team"))
		reply(w, v, err)
	})
	mux.HandleFunc("GET /api/teams/{team}/agents/{name}/screen", func(w http.ResponseWriter, r *http.Request) {
		a, err := c.GetAgent(r.Context(), r.PathValue("team"), r.PathValue("name"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		history, _ := strconv.Atoi(r.URL.Query().Get("history"))
		text, err := proc.Screen(a, history)
		reply(w, map[string]string{"screen": text}, err)
	})
	return mux
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		status := http.StatusInternalServerError
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			status = apiErr.Status
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}
