package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
)

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	team, name, user := r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user")
	out, err := s.store.GetSession(r.Context(), team, name, user)
	if err == nil && out.SessionID == "" && capturesSession(out.Client) {
		// Codex, dsh and opencode may not report their id; the daemon finds it in their files.
		if captured, cerr := s.captureSession(r.Context(), team, name); cerr == nil && captured.User == user {
			out = captured
		}
	}
	respond(w, out, err)
}

func (s *Server) saveSession(w http.ResponseWriter, r *http.Request) {
	var in model.Session
	if !decode(w, r, &in) {
		return
	}
	in.Team, in.Agent, in.User = r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user")
	out, err := s.store.SaveSession(r.Context(), in)
	if err == nil {
		err = s.memoryFiles().RememberSession(model.MemoryIdentity{Team: in.Team, Name: in.Agent, User: in.User}, out)
	}
	respond(w, out, err)
}

// clearSessions deletes one agent's saved session, or the whole team's when
// the route names no agent.
func (s *Server) clearSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearSessions(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user"))
	respond(w, map[string]int{"cleared": n}, err)
}

func (s *Server) requestAgentStop(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.RequestAgentStop(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("run_id"))
	respond(w, out, err)
}

func (s *Server) stopTeamAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.StopTeamAgents(r.Context(), r.PathValue("team"))
	respond(w, out, err)
}
