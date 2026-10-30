package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (s *Server) planRoutes() {
	m := s.mux
	m.HandleFunc("GET /v1/teams/{team}/plans", s.listPlans)
	m.HandleFunc("POST /v1/teams/{team}/plans", s.submitPlan)
	m.HandleFunc("GET /v1/teams/{team}/plans/{id}", s.getPlan)
	m.HandleFunc("POST /v1/teams/{team}/plans/{id}/approve", s.decidePlan(true))
	m.HandleFunc("POST /v1/teams/{team}/plans/{id}/reject", s.decidePlan(false))
	m.HandleFunc("PUT /v1/teams/{team}/members/{name}/plan-required", s.setPlanRequired)
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	plans, err := s.store.ListPlans(r.Context(), r.PathValue("team"), q.Get("agent"), model.PlanStatus(q.Get("status")))
	respond(w, plans, err)
}

func (s *Server) submitPlan(w http.ResponseWriter, r *http.Request) {
	var in store.SubmitPlanInput
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	plan, err := s.store.SubmitPlan(r.Context(), team, in)
	if err == nil {
		s.log.Info("plan submitted", "team", team, "plan", plan.ID, "agent", plan.Agent, "chars", len([]rune(plan.Body)))
	}
	respond(w, plan, err)
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.store.GetPlan(r.Context(), r.PathValue("team"), r.PathValue("id"))
	respond(w, plan, err)
}

func (s *Server) decidePlan(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in store.DecidePlanInput
		if !decode(w, r, &in) {
			return
		}
		team := r.PathValue("team")
		plan, err := s.store.DecidePlan(r.Context(), team, r.PathValue("id"), approve, in)
		if err == nil {
			s.log.Info("plan decided", "team", team, "plan", plan.ID, "agent", plan.Agent, "status", plan.Status, "by", plan.DecidedBy)
		}
		respond(w, plan, err)
	}
}

func (s *Server) setPlanRequired(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User     string `json:"user"`
		Required bool   `json:"required"`
	}
	if !decode(w, r, &in) {
		return
	}
	team, name := r.PathValue("team"), r.PathValue("name")
	err := s.store.SetPlanRequired(r.Context(), team, name, in.User, in.Required)
	if err == nil {
		s.log.Info("plan requirement changed", "team", team, "member", name, "required", in.Required)
	}
	respond(w, map[string]any{"team": team, "member": name, "planRequired": in.Required}, err)
}
