package server

import (
	"context"
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/runner"
)

// Restore resumes, in every team, the agents a restart left behind: not
// asked to stop, recorded as running or as interrupted (their supervisor
// was killed by the shutdown, or the reaper found them gone), with their
// process gone and a saved session. An agent stopped with agent stop, or
// whose supervisor gave up after its own exit code, stays down. One restore
// runs at a time, so a caller that asks while the daemon's own restore runs
// waits for it and finds those agents alive.
func (s *Server) Restore(ctx context.Context) ([]model.RestoredAgent, error) {
	s.restoreMu.Lock()
	defer s.restoreMu.Unlock()
	agents, err := s.store.RestorableAgents(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := s.store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.RestoredAgent{}
	for _, a := range agents {
		if alive, known := s.store.Probe(a); !known || alive {
			continue
		}
		if sessions[a.Team+"/"+a.Name].SessionID == "" {
			continue
		}
		r := model.RestoredAgent{Team: a.Team, Agent: a.Name, Status: "resumed"}
		if _, err := runner.Resume(ctx, s.runEnv(), runner.ResumeRequest{Team: a.Team, Name: a.Name, Replace: true}); err != nil {
			r.Status, r.Error = "failed", err.Error()
			s.log.Warn("agent restore failed", "team", a.Team, "agent", a.Name, "err", err)
		} else {
			s.log.Info("agent restored", "team", a.Team, "agent", a.Name)
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Server) restoreAgents(w http.ResponseWriter, r *http.Request) {
	restored, err := s.Restore(r.Context())
	respond(w, restored, err)
}
