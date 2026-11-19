package board

import (
	"encoding/json"
	"net/http"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
)

func vikunjaRoutes(mux *http.ServeMux, c *client.Client) {
	mux.HandleFunc("GET /api/settings/vikunja", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		settings, err := c.VikunjaSettings(r.Context())
		reply(w, settings, err)
	})
	mux.HandleFunc("PUT /api/settings/vikunja/{team}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var input model.VikunjaHookUpdate
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			reply(w, nil, &client.APIError{Status: http.StatusBadRequest, Msg: "Invalid Vikunja webhook settings"})
			return
		}
		settings, err := c.SetVikunjaSettings(r.Context(), r.PathValue("team"), input)
		reply(w, settings, err)
	})
}
