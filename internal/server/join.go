package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (s *Server) joinRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/teams/{team}/join-requests", s.listJoinRequests)
	m.HandleFunc("POST /v1/teams/{team}/join-requests", s.requestJoin)
	m.HandleFunc("GET /v1/teams/{team}/join-requests/{id}", s.getJoinRequest)
	m.HandleFunc("POST /v1/teams/{team}/join-requests/{id}/approve", s.approveJoin)
	m.HandleFunc("POST /v1/teams/{team}/join-requests/{id}/reject", s.rejectJoin)
}

func (s *Server) listJoinRequests(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListJoinRequests(r.Context(), r.PathValue("team"), model.JoinStatus(r.URL.Query().Get("status")))
	respond(w, out, err)
}

func (s *Server) requestJoin(w http.ResponseWriter, r *http.Request) {
	var in store.JoinRequestInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.RequestJoin(r.Context(), r.PathValue("team"), in)
	if err == nil {
		s.log.Info("join requested", "team", out.TeamName, "request", out.RequestID, "name", out.ProposedName)
	}
	respond(w, out, err)
}

func (s *Server) getJoinRequest(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetJoinRequest(r.Context(), r.PathValue("team"), r.PathValue("id"))
	respond(w, out, err)
}

func (s *Server) approveJoin(w http.ResponseWriter, r *http.Request) {
	var in store.JoinDecisionInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.ApproveJoin(r.Context(), r.PathValue("team"), r.PathValue("id"), in)
	if err == nil {
		s.log.Info("join approved", "team", out.TeamName, "request", out.RequestID, "member", out.AssignedName, "by", in.Caller)
	}
	respond(w, out, err)
}

func (s *Server) rejectJoin(w http.ResponseWriter, r *http.Request) {
	var in store.JoinDecisionInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.RejectJoin(r.Context(), r.PathValue("team"), r.PathValue("id"), in)
	if err == nil {
		s.log.Info("join rejected", "team", out.TeamName, "request", out.RequestID, "by", in.Caller)
	}
	respond(w, out, err)
}
