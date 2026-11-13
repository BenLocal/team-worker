package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/usage"
)

// The daemon owns the agents' processes and their CLIs' session files: the
// operations here run on its machine, so the CLI is the same client whether
// it reaches the daemon over the socket or over HTTP.

// memberUser returns the user a member's sessions are saved under.
func (s *Server) memberUser(ctx context.Context, team, name string) string {
	t, err := s.store.GetTeam(ctx, team)
	if err != nil {
		return ""
	}
	for _, m := range t.Members {
		if m.Name == name {
			return m.User
		}
	}
	return ""
}

// captureSession fills in a session id the CLI could not be given before
// it started: Codex's, matched in its transcripts by workspace and agent
// identity, and dsh's, matched in its session logs the same way. Other
// CLIs know their id from the start.
func (s *Server) captureSession(ctx context.Context, team, name string) (model.Session, error) {
	session, err := s.store.GetSession(ctx, team, name, s.memberUser(ctx, team, name))
	if err != nil || session.SessionID != "" || !capturesSession(session.Client) {
		return session, err
	}
	rec, err := s.store.GetAgent(ctx, team, name)
	if err != nil || rec.RunID != session.RunID {
		return session, nil
	}
	started, err := time.Parse(time.RFC3339Nano, rec.StartedAt)
	if err != nil {
		return session, nil
	}
	var id string
	switch session.Client {
	case "codex":
		root, err := cliHome("CODEX_HOME", ".codex")
		if err != nil {
			return session, err
		}
		id, err = agent.CaptureCodexSession(filepath.Join(root, "sessions"), session.Cwd, rec.AgentID, started)
		if err != nil {
			return session, err
		}
	case "dsh":
		root, err := cliHome("DSH_HOME", ".dsh")
		if err != nil {
			return session, err
		}
		id, err = agent.CaptureDshSession(filepath.Join(root, "sessions"), session.Cwd, rec.AgentID, started)
		if err != nil {
			return session, err
		}
	case "opencode":
		// opencode keeps its database under the XDG data directory.
		root, err := cliHome("XDG_DATA_HOME", filepath.Join(".local", "share"))
		if err != nil {
			return session, err
		}
		id, err = agent.CaptureOpencodeSession(ctx, filepath.Join(root, "opencode", "opencode.db"), session.Cwd, rec.AgentID, started)
		if err != nil {
			return session, err
		}
	}
	if id == "" {
		return session, nil
	}
	session.SessionID, session.Source = id, "transcript"
	saved, err := s.store.SaveSession(ctx, session)
	if err == nil {
		if memoryErr := s.memoryFiles().RememberSession(model.MemoryIdentity{Team: saved.Team, Name: saved.Agent, User: saved.User}, saved); memoryErr != nil {
			s.log.Warn("remember captured session", "err", memoryErr)
		}
	}
	return saved, err
}

// capturesSession reports whether the daemon finds a client's session id in
// its files, for CLIs that may not report it themselves.
func capturesSession(client string) bool {
	return client == "codex" || client == "dsh" || client == "opencode"
}

// cliHome is where a CLI keeps its state: the directory env names, or dir in
// the daemon's home.
func cliHome(env, dir string) (string, error) {
	if root := os.Getenv(env); root != "" {
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir), nil
}

// terminate stops an agent: it saves the session id, records the stop as
// intended so keepalive does not restart it, ends the process and releases
// the agent's tasks.
func (s *Server) terminate(ctx context.Context, team, name string) (model.AgentExit, error) {
	a, err := s.store.GetAgent(ctx, team, name)
	if err != nil {
		return model.AgentExit{}, err
	}
	if _, err := s.captureSession(ctx, team, name); err != nil && store.KindOf(err) != store.KindNotFound {
		s.log.Warn("capture session", "team", team, "agent", name, "err", err)
	}
	if _, err := s.store.RequestAgentStop(ctx, team, name, a.RunID); err != nil {
		return model.AgentExit{}, err
	}
	if a.Status == model.AgentRunning {
		if err := proc.Stop(a); err != nil {
			return model.AgentExit{}, err
		}
	}
	return s.store.AgentExitedRun(ctx, team, name, a.RunID, 0)
}

func (s *Server) terminateAgent(w http.ResponseWriter, r *http.Request) {
	out, err := s.terminate(r.Context(), r.PathValue("team"), r.PathValue("name"))
	if err == nil {
		s.log.Info("agent stopped", "team", r.PathValue("team"), "agent", r.PathValue("name"), "released", len(out.Released))
	}
	respond(w, out, err)
}

func (s *Server) agentScreen(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAgent(r.Context(), r.PathValue("team"), r.PathValue("name"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	text, err := proc.Screen(a, lines)
	respond(w, map[string]string{"agent": a.Name, "screen": text}, err)
}

// syncCosts records the token totals of the team's agents' native sessions,
// or of one agent's, read from this machine's session files.
func (s *Server) syncCosts(w http.ResponseWriter, r *http.Request) {
	team, only := r.PathValue("team"), r.URL.Query().Get("agent")
	agents, err := s.store.ListAgents(r.Context(), team)
	if err != nil {
		respond(w, nil, err)
		return
	}
	recorded := []model.CostEvent{}
	for _, a := range agents {
		if only != "" && a.Name != only {
			continue
		}
		session, err := s.captureSession(r.Context(), team, a.Name)
		if store.KindOf(err) == store.KindNotFound {
			continue
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		t, ok, err := usage.Session(session.Client, session.SessionID)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if !ok {
			continue
		}
		e, err := s.store.RecordSessionUsage(r.Context(), team, model.CostEvent{
			AgentName: a.Name, Provider: t.Provider, Model: t.Model, SessionID: session.SessionID,
			InputTokens: t.Input, OutputTokens: t.Output,
		})
		if err != nil {
			respond(w, nil, err)
			return
		}
		recorded = append(recorded, e)
	}
	respond(w, map[string]any{"team": team, "recorded": recorded}, nil)
}
