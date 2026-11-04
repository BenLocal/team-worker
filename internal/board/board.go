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
)

// dist holds the Vite build of the page (`make web`, not committed); all:
// keeps files Vite names with a leading underscore.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the board page, a small JSON API backed by the daemon and
// the live terminal endpoints.
// Handler serves the board page and its API from c. terminals serves the
// live terminal endpoints, or is nil on a board that does not run where the
// agents run: the page then shows each agent's screen from the daemon.
func Handler(c *client.Client, terminals http.Handler) http.Handler {
	return newHandler(c, terminals)
}

func newHandler(c *client.Client, terminals http.Handler) http.Handler {
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
	mux.HandleFunc("GET /api/service-keys", serviceKeys)
	mux.HandleFunc("GET /api/service-auth/magpie", magpieAuth)
	mux.HandleFunc("GET /api/openbao/login", openbaoLogin)
	if terminals != nil {
		mux.Handle("POST /api/terminal/ticket", terminals)
		mux.Handle("GET /api/terminal/ws", terminals)
	} else {
		noTerminals := func(w http.ResponseWriter, r *http.Request) {
			reply(w, nil, &client.APIError{Status: http.StatusNotFound,
				Msg: "Live terminals are only on the board team-workerd serves (team-workerd --board)"})
		}
		mux.HandleFunc("POST /api/terminal/ticket", noTerminals)
		mux.HandleFunc("GET /api/terminal/ws", noTerminals)
	}
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
	// Host tells the page whether this board has live terminals.
	mux.HandleFunc("GET /api/host", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"terminals": terminals != nil}, nil)
	})
	// Oversight is what waits on the leader or limits the team: plans, join
	// requests and spending. It is polled separately from the snapshot.
	mux.HandleFunc("GET /api/teams/{team}/oversight", func(w http.ResponseWriter, r *http.Request) {
		v, err := oversight(r.Context(), c, r.PathValue("team"))
		reply(w, v, err)
	})
	// The daemon reads the screen on its machine, wherever this board runs.
	mux.HandleFunc("GET /api/teams/{team}/agents/{name}/screen", func(w http.ResponseWriter, r *http.Request) {
		history, _ := strconv.Atoi(r.URL.Query().Get("history"))
		text, err := c.AgentScreen(r.Context(), r.PathValue("team"), r.PathValue("name"), history)
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
