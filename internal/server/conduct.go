package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/runner"
)

// conductors are the harness conductors the daemon runs, one per team. A
// conductor lives until its run ends, it is stopped, or the daemon stops;
// after a daemon restart, `harness conduct` starts it again.
type conductors struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
}

func (s *Server) startConducting(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	var in struct {
		PollSeconds float64 `json:"pollSeconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	poll := time.Duration(in.PollSeconds * float64(time.Second))
	if poll <= 0 {
		poll = 5 * time.Second
	}
	st, err := s.store.HarnessStatus(r.Context(), team, "")
	if err != nil {
		respond(w, nil, err)
		return
	}
	if st.Status != model.HarnessRunning {
		writeError(w, http.StatusConflict, fmt.Sprintf("harness run %s is %s; pass --goal to start a new one", st.ID, st.Status))
		return
	}
	out := model.Conducting{Team: team, Run: st.ID, Conducting: true}
	c := &s.conduct
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.running[team]; !ok {
		ctx, cancel := context.WithCancel(context.Background())
		c.running[team] = cancel
		out.Started = true
		cd := runner.NewConductor(s.runEnv(), team, poll, s.log)
		c.wg.Go(func() {
			cd.Run(ctx)
			c.mu.Lock()
			delete(c.running, team)
			c.mu.Unlock()
			cancel()
		})
		s.log.Info("harness conductor started", "team", team, "run", st.ID)
	}
	respond(w, out, nil)
}

func (s *Server) stopConducting(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	c := &s.conduct
	c.mu.Lock()
	cancel, ok := c.running[team]
	c.mu.Unlock()
	if ok {
		cancel()
	}
	respond(w, map[string]bool{"stopped": ok}, nil)
}

// Close stops the harness conductors and waits for them.
func (s *Server) Close() {
	c := &s.conduct
	c.mu.Lock()
	for _, cancel := range c.running {
		cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}
