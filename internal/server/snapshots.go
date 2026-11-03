package server

import "net/http"

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tag string `json:"tag"`
	}
	if !decode(w, r, &in) {
		return
	}
	meta, err := s.store.CreateSnapshot(r.Context(), r.PathValue("team"), in.Tag)
	if err == nil {
		s.log.Info("snapshot created", "team", meta.TeamName, "id", meta.ID)
	}
	respond(w, meta, err)
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSnapshots(r.Context(), r.PathValue("team"))
	respond(w, list, err)
}

func (s *Server) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun bool `json:"dryRun"`
	}
	if !decode(w, r, &in) {
		return
	}
	team, id := r.PathValue("team"), r.PathValue("id")
	sum, err := s.store.RestoreSnapshot(r.Context(), team, id, in.DryRun)
	if err == nil && !in.DryRun {
		s.log.Info("snapshot restored", "team", team, "id", id)
	}
	respond(w, sum, err)
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	team, id := r.PathValue("team"), r.PathValue("id")
	err := s.store.DeleteSnapshot(r.Context(), team, id)
	respond(w, map[string]string{"status": "deleted", "id": id}, err)
}
