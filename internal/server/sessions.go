package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
)

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetSession(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user"))
	respond(w, out, err)
}

func (s *Server) saveSession(w http.ResponseWriter, r *http.Request) {
	var in model.Session
	if !decode(w, r, &in) {
		return
	}
	in.Team, in.Agent, in.User = r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user")
	out, err := s.store.SaveSession(r.Context(), in)
	respond(w, out, err)
}

func (s *Server) requestAgentStop(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.RequestAgentStop(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("run_id"))
	respond(w, out, err)
}

func (s *Server) stopTeamAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.StopTeamAgents(r.Context(), r.PathValue("team"))
	respond(w, out, err)
}
