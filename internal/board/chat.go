package board

import (
	"encoding/json"
	"net/http"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func chatRoutes(mux *http.ServeMux, c *client.Client) {
	mux.HandleFunc("GET /api/chat/blocked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		items, err := c.BlockedChatRequests(r.Context())
		reply(w, items, err)
	})
	mux.HandleFunc("GET /api/chat/feishu/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		info, err := c.FeishuApp(r.Context())
		reply(w, info, err)
	})
	mux.HandleFunc("PUT /api/chat/feishu/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var app config.FeishuApp
		if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
			reply(w, nil, &client.APIError{Status: http.StatusBadRequest, Msg: "Invalid Feishu credentials"})
			return
		}
		info, err := c.SetFeishuApp(r.Context(), app)
		reply(w, info, err)
	})
	mux.HandleFunc("GET /api/chat/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		settings, err := c.ChatSettings(r.Context())
		reply(w, settings, err)
	})
	mux.HandleFunc("PUT /api/chat/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings model.ChatSettings
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			reply(w, nil, &client.APIError{Status: http.StatusBadRequest, Msg: "Invalid chat settings"})
			return
		}
		settings, err := c.SetChatSettings(r.Context(), settings)
		reply(w, settings, err)
	})
	mux.HandleFunc("GET /api/chat/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		connections, err := c.ChatConnections(r.Context())
		reply(w, connections, err)
	})
}
