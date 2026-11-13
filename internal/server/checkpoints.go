package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/benlocal/team-worker/internal/store"
)

func (s *Server) checkpointRoutes() {
	s.mux.HandleFunc("GET /v1/teams/{team}/checkpoints", s.listCheckpoints)
	s.mux.HandleFunc("POST /v1/teams/{team}/checkpoints", s.createCheckpoint)
	s.mux.HandleFunc("GET /v1/teams/{team}/checkpoints/{id}", s.getCheckpoint)
	s.mux.HandleFunc("POST /v1/teams/{team}/checkpoints/{id}", s.updateCheckpoint)
}
func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListCheckpoints(r.Context(), r.PathValue("team"))
	respond(w, out, err)
}
func (s *Server) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	var in store.CreateCheckpointInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.CreateCheckpoint(r.Context(), r.PathValue("team"), in)
	respond(w, out, err)
}
func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetCheckpoint(r.Context(), r.PathValue("team"), r.PathValue("id"))
	respond(w, out, err)
}
func (s *Server) updateCheckpoint(w http.ResponseWriter, r *http.Request) {
	var in store.CheckpointUpdateInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.store.UpdateCheckpoint(r.Context(), r.PathValue("team"), r.PathValue("id"), in)
	respond(w, out, err)
}

// runCheckpoints scans persisted deadlines at startup and once per second.
// The daemon's worker group cancels and joins it during shutdown.
func runCheckpoints(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := st.AdvanceCheckpoints(ctx); err != nil && ctx.Err() == nil {
			log.Warn("advance checkpoints", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
