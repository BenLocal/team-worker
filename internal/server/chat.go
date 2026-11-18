package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func (s *Server) chatRoutes() {
	s.mux.HandleFunc("POST /v1/teams/{team}/chat/requests/{id}/results", func(w http.ResponseWriter, r *http.Request) {
		if local, _ := r.Context().Value(localAccess{}).(bool); !local {
			writeError(w, http.StatusForbidden, "Run this command from the current Leader's local session")
			return
		}
		var result model.ChatResultInput
		r.Body = http.MaxBytesReader(w, r.Body, model.ChatResultBodyLimit)
		if !decode(w, r, &result) {
			return
		}
		err := s.store.SubmitChatResult(r.Context(), r.PathValue("team"), r.PathValue("id"), result)
		respond(w, map[string]bool{"queued": true}, err)
	})
	s.mux.HandleFunc("GET /v1/chat/blocked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		items, err := s.store.BlockedChatRequests(r.Context())
		respond(w, items, err)
	})
	s.mux.HandleFunc("GET /v1/chat/feishu/app", s.feishuApp)
	s.mux.HandleFunc("PUT /v1/chat/feishu/app", s.feishuApp)
	s.mux.HandleFunc("GET /v1/chat/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		settings, err := s.store.ChatSettings(r.Context())
		respond(w, settings, err)
	})
	s.mux.HandleFunc("PUT /v1/chat/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings model.ChatSettings
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		if !decode(w, r, &settings) {
			return
		}
		saved, err := s.store.SetChatSettings(r.Context(), settings)
		respond(w, saved, err)
	})
	s.mux.HandleFunc("GET /v1/chat/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		connections, err := s.store.ChatConnections(r.Context())
		respond(w, connections, err)
	})
	s.mux.HandleFunc("POST /v1/chat/connections", func(w http.ResponseWriter, r *http.Request) {
		var connection model.ChatConnection
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if !decode(w, r, &connection) {
			return
		}
		err := s.store.ReportChatConnection(r.Context(), connection)
		respond(w, map[string]bool{"ok": true}, err)
	})
}

func (s *Server) feishuApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if local, _ := r.Context().Value(localAccess{}).(bool); !local {
		writeError(w, http.StatusForbidden, "Use the daemon's authenticated Web board to manage Feishu credentials")
		return
	}
	dir := s.runDataDir
	if dir == "" {
		dir = config.DataDir()
	}
	info := model.FeishuAppInfo{ManagedByEnvironment: config.FeishuManagedByEnvironment()}
	if r.Method == http.MethodPut {
		if info.ManagedByEnvironment {
			writeError(w, http.StatusConflict, "Feishu credentials are managed by environment variables")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var app config.FeishuApp
		if !decode(w, r, &app) {
			return
		}
		if err := app.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := config.SaveFeishuApp(dir, app); err != nil {
			writeError(w, http.StatusInternalServerError, "Unable to save Feishu credentials")
			return
		}
		info.AppID = app.AppID
	} else {
		app, err := config.ReadFeishuApp(dir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Unable to read the Feishu application configuration")
			return
		}
		info.AppID = app.AppID
	}
	writeJSON(w, http.StatusOK, info)
}
