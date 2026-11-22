package server

import (
	"errors"
	"net/http"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func (s *Server) vikunjaSettingsRoutes() {
	s.mux.HandleFunc("GET /v1/hooks/vikunja/settings", s.vikunjaSettings)
	s.mux.HandleFunc("PUT /v1/hooks/vikunja/settings/{team}", s.vikunjaSettings)
}

func (s *Server) vikunjaSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if local, _ := r.Context().Value(localAccess{}).(bool); !local {
		writeError(w, http.StatusForbidden, "Use the daemon's authenticated Web board to manage Vikunja webhooks")
		return
	}
	if r.Method == http.MethodPut {
		s.saveVikunjaSettings(w, r)
		return
	}
	hooks, err := config.ReadVikunjaHooks(s.vikunjaDataDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Unable to read Vikunja webhook settings")
		return
	}
	teams, err := s.store.ListTeams(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	settings := make([]model.VikunjaHookSettings, 0, len(teams))
	for _, team := range teams {
		settings = append(settings, vikunjaSettingsInfo(team.Name, hooks[team.Name]))
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) saveVikunjaSettings(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if _, err := s.store.GetTeam(r.Context(), team); err != nil {
		respond(w, nil, err)
		return
	}
	var input model.VikunjaHookUpdate
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if !decode(w, r, &input) {
		return
	}
	s.vikunjaSettingsMu.Lock()
	defer s.vikunjaSettingsMu.Unlock()
	dir := s.vikunjaDataDir()
	hooks, err := config.ReadVikunjaHooks(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Unable to read Vikunja webhook settings")
		return
	}
	if hooks == nil {
		hooks = make(map[string]config.VikunjaHook)
	}
	hook := hooks[team]
	if input.Secret != "" {
		hook.Secret = input.Secret
	}
	hook.Events = input.Events
	if input.Commands != nil {
		hook.Commands = input.Commands
	}
	hooks[team] = hook
	if err := config.SaveVikunjaHooks(dir, hooks); err != nil {
		if errors.Is(err, config.ErrInvalidVikunjaHooks) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "Unable to save Vikunja webhook settings")
		}
		return
	}
	writeJSON(w, http.StatusOK, vikunjaSettingsInfo(team, hook))
}

func vikunjaSettingsInfo(team string, hook config.VikunjaHook) model.VikunjaHookSettings {
	return model.VikunjaHookSettings{Team: team, HasSecret: hook.Secret != "", Events: hook.Events, Commands: hook.Commands}
}
