package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
)

func (s *Server) costRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/teams/{team}/costs", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.store.Costs(r.Context(), r.PathValue("team"), r.URL.Query().Get("agent"))
		respond(w, out, err)
	})
	m.HandleFunc("POST /v1/teams/{team}/costs", func(w http.ResponseWriter, r *http.Request) {
		var in model.CostEvent
		if !decode(w, r, &in) {
			return
		}
		out, err := s.store.ReportCost(r.Context(), r.PathValue("team"), in)
		respond(w, out, err)
	})
	m.HandleFunc("PUT /v1/teams/{team}/costs/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in model.CostEvent
		if !decode(w, r, &in) {
			return
		}
		out, err := s.store.RecordSessionUsage(r.Context(), r.PathValue("team"), in)
		respond(w, out, err)
	})
	m.HandleFunc("GET /v1/teams/{team}/budget", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.store.Budget(r.Context(), r.PathValue("team"))
		respond(w, out, err)
	})
	m.HandleFunc("PUT /v1/teams/{team}/budget", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			BudgetCents float64 `json:"budgetCents"`
			Enforce     bool    `json:"enforce"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := s.store.SetBudget(r.Context(), r.PathValue("team"), in.BudgetCents, in.Enforce)
		respond(w, out, err)
	})
}
