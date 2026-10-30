package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/store"
)

func (s *Server) harnessRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/teams/{team}/harness", s.listHarness)
	m.HandleFunc("POST /v1/teams/{team}/harness", s.startHarness)
	m.HandleFunc("GET /v1/teams/{team}/harness/current", s.harnessStatus)
	m.HandleFunc("GET /v1/teams/{team}/harness/{id}", s.harnessStatus)
	m.HandleFunc("POST /v1/teams/{team}/harness/{id}/advance", s.advanceHarness)
	m.HandleFunc("POST /v1/teams/{team}/harness/{id}/approve", s.approveHarness)
	m.HandleFunc("POST /v1/teams/{team}/harness/{id}/abort", s.abortHarness)
	m.HandleFunc("GET /v1/teams/{team}/harness/{id}/artifacts", s.listArtifacts)
	m.HandleFunc("GET /v1/teams/{team}/harness/{id}/artifacts/{name}", s.getArtifact)
	m.HandleFunc("PUT /v1/teams/{team}/harness/{id}/artifacts/{name}", s.putArtifact)
	m.HandleFunc("POST /v1/teams/{team}/harness/{id}/tasks", s.createHarnessTask)
	m.HandleFunc("POST /v1/teams/{team}/harness/{id}/agents", s.addHarnessAgent)
}

// harnessID maps the "current" path segment to an empty id, which the
// store resolves to the running (or newest) run.
func harnessID(r *http.Request) string {
	if id := r.PathValue("id"); id != "current" {
		return id
	}
	return ""
}

func (s *Server) listHarness(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListHarness(r.Context(), r.PathValue("team"))
	respond(w, runs, err)
}

func (s *Server) startHarness(w http.ResponseWriter, r *http.Request) {
	var in store.StartHarnessInput
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	run, err := s.store.StartHarness(r.Context(), team, in)
	if err == nil {
		s.log.Info("harness started", "team", team, "harness", run.ID, "cli", run.CLI, "agents", run.AgentCount)
	}
	respond(w, run, err)
}

func (s *Server) harnessStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.HarnessStatus(r.Context(), r.PathValue("team"), harnessID(r))
	respond(w, st, err)
}

func (s *Server) advanceHarness(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	out, err := s.store.AdvanceHarness(r.Context(), team, harnessID(r))
	if err == nil {
		s.log.Info("harness advanced", "team", team, "harness", out.ID, "from", out.From, "to", out.Phase,
			"status", out.Status, "rolled_back", out.RolledBack, "attempt", out.Attempt)
	}
	respond(w, out, err)
}

func (s *Server) approveHarness(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Caller   string `json:"caller"`
		Feedback string `json:"feedback"`
	}
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	plan, err := s.store.ApproveHarnessPlan(r.Context(), team, harnessID(r), in.Caller, in.Feedback)
	if err == nil {
		s.log.Info("harness plan approved", "team", team, "plan", plan.ID, "agent", plan.Agent, "by", plan.DecidedBy)
	}
	respond(w, plan, err)
}

func (s *Server) abortHarness(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	st, err := s.store.AbortHarness(r.Context(), team, harnessID(r), in.Reason)
	if err == nil {
		s.log.Info("harness aborted", "team", team, "harness", st.ID, "phase", st.Phase)
	}
	respond(w, st, err)
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	arts, err := s.store.ListArtifacts(r.Context(), r.PathValue("team"), harnessID(r))
	respond(w, arts, err)
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetArtifact(r.Context(), r.PathValue("team"), harnessID(r), r.PathValue("name"))
	respond(w, a, err)
}

func (s *Server) putArtifact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &in) {
		return
	}
	team := r.PathValue("team")
	a, err := s.store.PutArtifact(r.Context(), team, harnessID(r), r.PathValue("name"), in.Content)
	if err == nil {
		s.log.Info("harness artifact registered", "team", team, "artifact", a.Name, "bytes", a.Size)
	}
	respond(w, a, err)
}

func (s *Server) createHarnessTask(w http.ResponseWriter, r *http.Request) {
	var in store.CreateTaskInput
	if !decode(w, r, &in) {
		return
	}
	task, err := s.store.CreateHarnessTask(r.Context(), r.PathValue("team"), harnessID(r), in)
	respond(w, task, err)
}

func (s *Server) addHarnessAgent(w http.ResponseWriter, r *http.Request) {
	var in store.HarnessAgentInput
	if !decode(w, r, &in) {
		return
	}
	a, err := s.store.AddHarnessAgent(r.Context(), r.PathValue("team"), harnessID(r), in)
	respond(w, a, err)
}
