package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/profile"
	"github.com/benlocal/team-worker/internal/runner"
)

// Profiles and presets live in the daemon's config.json: the daemon starts
// the agents that use them, and only names of variables are stored there.

func (s *Server) getProfiles(w http.ResponseWriter, r *http.Request) {
	cfg, err := profile.Load(config.FilePath())
	respondRun(w, cfg, err)
}

func (s *Server) saveProfiles(w http.ResponseWriter, r *http.Request) {
	var cfg profile.Config
	if !decode(w, r, &cfg) {
		return
	}
	err := profile.Save(config.FilePath(), cfg)
	if err == nil {
		s.log.Info("profiles saved", "profiles", len(cfg.Profiles), "presets", len(cfg.Presets))
	}
	respondRun(w, map[string]bool{"saved": err == nil}, err)
}

func (s *Server) testProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Prompt string `json:"prompt"`
		Cwd    string `json:"cwd"`
	}
	if !decode(w, r, &in) {
		return
	}
	res, err := runner.TestProfile(r.Context(), r.PathValue("name"), in.Prompt, in.Cwd)
	respondRun(w, res, err)
}
