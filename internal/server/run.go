package server

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/runner"
)

// SetRunner tells the server where agents it starts keep their data and
// which team-worker CLI their hooks and supervisors run. Empty values take
// the configured data directory and the CLI next to this executable.
func (s *Server) SetRunner(dataDir, workerBin string) {
	s.runDataDir, s.workerBin = dataDir, workerBin
}

// runEnv is what the runner gets: this server's API in process, so spawning
// goes through the same checks as any client.
func (s *Server) runEnv() runner.Env {
	dataDir := s.runDataDir
	if dataDir == "" {
		dataDir = config.DataDir()
	}
	bin := s.workerBin
	if bin == "" {
		bin = workerBin()
	}
	return runner.Env{Client: LocalClient(s), DataDir: dataDir, WorkerBin: bin}
}

// workerBin finds the team-worker CLI: next to this executable, then in PATH.
func workerBin() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if p := filepath.Join(filepath.Dir(exe), "team-worker"); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("team-worker"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return "team-worker"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// respondRun keeps the status of an API error the runner met and reports
// the runner's own refusals, such as an unsupported CLI, as bad requests.
func respondRun(w http.ResponseWriter, v any, err error) {
	var apiErr *client.APIError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.As(err, &apiErr):
		writeError(w, apiErr.Status, apiErr.Msg)
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) spawnAgent(w http.ResponseWriter, r *http.Request) {
	var req runner.Request
	if !decode(w, r, &req) {
		return
	}
	req.Team, req.Name = r.PathValue("team"), r.PathValue("name")
	// The profile also supplies the command when none was given.
	if err := runner.ApplyProfile(&req, req.Profile); err != nil {
		respondRun(w, nil, err)
		return
	}
	res, err := runner.Spawn(r.Context(), s.runEnv(), req)
	if err == nil && !req.DryRun {
		s.log.Info("agent spawned", "team", req.Team, "agent", req.Name, "backend", req.Backend)
	}
	respondRun(w, res, err)
}

func (s *Server) resumeAgent(w http.ResponseWriter, r *http.Request) {
	var in runner.ResumeRequest
	if !decode(w, r, &in) {
		return
	}
	in.Team, in.Name = r.PathValue("team"), r.PathValue("name")
	res, err := runner.Resume(r.Context(), s.runEnv(), in)
	if err == nil && !in.DryRun {
		s.log.Info("agent resumed", "team", in.Team, "agent", in.Name)
	}
	respondRun(w, res, err)
}

func (s *Server) launchTeam(w http.ResponseWriter, r *http.Request) {
	var in runner.LaunchRequest
	if !decode(w, r, &in) {
		return
	}
	res, err := runner.Launch(r.Context(), s.runEnv(), in)
	if err == nil {
		s.log.Info("team launched", "team", res.Team, "template", res.Template, "status", res.Status)
	}
	respondRun(w, res, err)
}
