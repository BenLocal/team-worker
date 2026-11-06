package server

import (
	"net/http"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (s *Server) decisionRoutes() {
	m := s.mux
	m.HandleFunc("GET /v1/teams/{team}/requests", s.listDecisions)
	m.HandleFunc("POST /v1/teams/{team}/requests", s.createDecision)
	m.HandleFunc("GET /v1/teams/{team}/requests/{id}", s.getDecision)
	m.HandleFunc("POST /v1/teams/{team}/requests/{id}/decide", s.decideRequest)
	m.HandleFunc("GET /v1/teams/{team}/auto-review", s.getAutoReview)
	m.HandleFunc("PUT /v1/teams/{team}/auto-review", s.setAutoReview)
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListDecisionRequests(r.Context(), r.PathValue("team"), model.DecisionStatus(r.URL.Query().Get("status")))
	respond(w, list, err)
}

func (s *Server) createDecision(w http.ResponseWriter, r *http.Request) {
	var in store.CreateDecisionInput
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	req, err := s.store.CreateDecisionRequest(r.Context(), team, in)
	if err == nil {
		s.log.Info("decision requested", "team", team, "request", req.ID, "from", req.From,
			"options", len(req.Options), "human_only", req.HumanOnly)
	}
	respond(w, req, err)
}

func (s *Server) getDecision(w http.ResponseWriter, r *http.Request) {
	req, err := s.store.GetDecisionRequest(r.Context(), r.PathValue("team"), r.PathValue("id"))
	respond(w, req, err)
}

// decideRequest decides as the operator unless the body names another
// decider; the AI auto reviewer decides inside the daemon, never over HTTP.
func (s *Server) decideRequest(w http.ResponseWriter, r *http.Request) {
	var in store.DecideInput
	if !decode(w, r, &in) {
		return
	}
	if in.DecidedBy == "" {
		in.DecidedBy = store.OperatorInbox
	}
	team := r.PathValue("team")
	req, err := s.store.DecideRequest(r.Context(), team, r.PathValue("id"), in)
	if err == nil {
		s.log.Info("decision made", "team", team, "request", req.ID, "from", req.From, "status", req.Status,
			"choice", req.Choice, "by", req.DecidedBy)
	}
	respond(w, req, err)
}

func (s *Server) getAutoReview(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.AutoReviewSetting(r.Context(), r.PathValue("team"))
	respond(w, model.NewAutoReviewStatus(a, time.Now()), err)
}

func (s *Server) setAutoReview(w http.ResponseWriter, r *http.Request) {
	var in model.AutoReview
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	a, err := s.store.SetAutoReview(r.Context(), team, in)
	if err == nil {
		s.log.Info("auto review changed", "team", team, "enabled", a.Enabled, "model", a.Model,
			"from", a.From, "to", a.To, "timeZone", a.TimeZone)
	}
	respond(w, model.NewAutoReviewStatus(a, time.Now()), err)
}
