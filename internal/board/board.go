// Package board serves the read-only web board for watching teams.
package board

import (
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/proc"
)

//go:embed static
var static embed.FS

// Handler serves the board page and a small JSON API backed by the daemon.
func Handler(c *client.Client) http.Handler {
	mux := http.NewServeMux()
	page, _ := static.ReadFile("static/index.html")
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("GET /api/teams", func(w http.ResponseWriter, r *http.Request) {
		teams, err := c.ListTeams(r.Context())
		reply(w, teams, err)
	})
	mux.HandleFunc("GET /api/teams/{team}", func(w http.ResponseWriter, r *http.Request) {
		b, err := c.Board(r.Context(), r.PathValue("team"), 100)
		reply(w, b, err)
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
