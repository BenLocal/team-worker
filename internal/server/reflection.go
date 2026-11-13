package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/memory"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/runner"
	"github.com/benlocal/team-worker/internal/store"
)

func sessionIdentity(s model.Session) model.MemoryIdentity {
	return model.MemoryIdentity{User: s.User, Team: s.Team, Name: s.Agent}
}

func (s *Server) reflectionOperation(ctx context.Context, id model.MemoryIdentity, action string, in model.MemoryRequest) (model.MemoryResult, error) {
	files := s.memoryFiles()
	if err := files.Ensure(id); err != nil {
		return model.MemoryResult{}, err
	}
	if action == "reflect-config" || action == "reflect-status" {
		state, err := files.ReflectionState(id, in.Config)
		return model.MemoryResult{Config: &state.Config, Reflection: &state.Latest}, err
	}
	if action == "reflect-discard" {
		out, err := files.DiscardReflection(id, in.Revision)
		return model.MemoryResult{Reflection: &out}, err
	}
	if action == "reflect-apply" {
		out, err := s.applyReflection(ctx, id, in.Revision, false)
		return model.MemoryResult{Reflection: &out}, err
	}

	session, err := s.store.GetSession(ctx, id.Team, id.Name, id.User)
	if store.KindOf(err) == store.KindNotFound {
		session = model.Session{}
	} else if err != nil {
		return model.MemoryResult{}, err
	}
	reports, err := s.store.MemoryTaskEvidence(ctx)
	if err != nil {
		return model.MemoryResult{}, err
	}
	out, err := s.reflectMember(ctx, id, session, false, time.Now(), reports[id])
	return model.MemoryResult{Reflection: &out}, err
}

func transcriptEvidence(session model.Session) (string, error) {
	if session.SessionID == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var root string
	switch session.Client {
	case "claude":
		root = os.Getenv("CLAUDE_CONFIG_DIR")
		if root == "" {
			root = filepath.Join(home, ".claude")
		}
		root = filepath.Join(root, "projects")
	case "codex":
		root = os.Getenv("CODEX_HOME")
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		root = filepath.Join(root, "sessions")
	case "pi":
		root = os.Getenv("PI_CODING_AGENT_DIR")
		if root == "" {
			root = filepath.Join(home, ".pi", "agent")
		}
		root = filepath.Join(root, "sessions")
	default:
		return "", nil
	}
	return agent.TranscriptEvidence(root, session.Client, session.SessionID)
}

func (s *Server) reflectMember(ctx context.Context, id model.MemoryIdentity, session model.Session, scheduled bool, now time.Time, taskEvidence string) (model.Reflection, error) {
	s.memory.reflectMu.Lock()
	if s.memory.running == nil {
		s.memory.running = map[model.MemoryIdentity]bool{}
	}
	if s.memory.running[id] {
		s.memory.reflectMu.Unlock()
		return model.Reflection{}, memory.ErrConflict
	}
	s.memory.running[id] = true
	s.memory.reflectMu.Unlock()
	defer func() { s.memory.reflectMu.Lock(); delete(s.memory.running, id); s.memory.reflectMu.Unlock() }()
	files := s.memoryFiles()
	if scheduled {
		state, err := files.ReflectionState(id, nil)
		if err != nil {
			return model.Reflection{}, err
		}
		loc, err := time.LoadLocation(state.Config.Timezone)
		if err != nil {
			return model.Reflection{}, err
		}
		local := now.In(loc)
		doneToday := state.Day == local.Format("2006-01-02") &&
			(state.Attempts >= 3 || state.Latest.Status == "unchanged" || state.Latest.Status == "applied" || state.Latest.Status == "discarded")
		if !state.Config.Enabled || local.Hour() < state.Config.Hour || doneToday {
			return state.Latest, nil
		}
	}
	if err := files.RememberSession(id, session); err != nil {
		return model.Reflection{}, err
	}
	refs, err := files.SessionRefs(id)
	if err != nil {
		return model.Reflection{}, err
	}
	var evidenceText strings.Builder
	// Keep the newest sessions within a fixed evidence budget.
	for i := len(refs) - 1; i >= 0; i-- {
		text, e := transcriptEvidence(refs[i])
		if e != nil {
			return model.Reflection{}, e
		}
		if text == "" {
			continue
		}
		if evidenceText.Len()+len(text) > 384<<10 {
			break
		}
		fmt.Fprintf(&evidenceText, "\nSource %s/%s:\n%s", refs[i].Client, refs[i].SessionID, text)
	}
	evidence := evidenceText.String() + taskEvidence
	job, out, err := files.BeginReflection(id, evidence, scheduled, now)
	if err != nil || job.ID == "" {
		return out, err
	}
	if len(job.Config.Command) == 0 && job.Config.Profile == "" && len(session.Command) > 0 {
		job.Config.Command = session.Command
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(job.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	output, runErr := runner.Reflect(runCtx, job.Config, job.Prompt)
	return files.FinishReflection(id, job, output, runErr)
}

// runMemory watches files without typing into terminals. Hooks fetch updates
// at safe boundaries; executors without hooks reload on their next resume.
func (s *Server) runMemory(ctx context.Context) {
	ticker := time.NewTicker(config.MemoryPollInterval(s.runEnv().DataDir))
	defer ticker.Stop()
	for {
		if err := s.memoryTick(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("personal memory maintenance", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) memoryTick(ctx context.Context, now time.Time) error {
	files := s.memoryFiles()
	ids, err := files.Identities()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Bulk reads keep the scan independent of the number of members.
	_, version := s.hub.Changed()
	agents, err := s.store.AllAgents(ctx)
	if err != nil {
		return err
	}
	processes := map[[2]string]model.Agent{}
	for _, rec := range agents {
		processes[[2]string{rec.Team, rec.Name}] = rec
	}
	sessions, err := s.store.AllSessions(ctx)
	if err != nil {
		return err
	}
	reports, err := s.store.MemoryTaskEvidence(ctx)
	if err != nil {
		return err
	}
	saved := map[model.MemoryIdentity]model.Session{}
	for _, session := range sessions {
		saved[sessionIdentity(session)] = session
	}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		changed, pollErr := files.Poll(id)
		if pollErr != nil {
			s.log.Warn("observe personal documents", "err", pollErr)
			continue
		}
		if changed {
			s.hub.Notify()
		}
		state, e := files.ReflectionState(id, nil)
		if e != nil {
			return e
		}
		if !state.Config.Enabled {
			continue
		}
		if state.Latest.Status == "pending" || state.Latest.Status == "conflict" || state.Latest.Status == "partial" {
			if state.Config.AutoApply && state.Latest.Status == "pending" {
				rec, found := processes[[2]string{id.Team, id.Name}]
				if _, e = s.applyScheduledReflection(id, state.Latest.ID, version, rec, found, saved[id]); e != nil && !errors.Is(e, memory.ErrConflict) {
					s.log.Warn("apply scheduled reflection", "err", e)
				}
			}
			continue
		}
		if _, e = s.reflectMember(ctx, id, saved[id], true, now, reports[id]); e != nil {
			s.log.Warn("scheduled reflection", "err", e)
		}
	}
	return nil
}

func (s *Server) applyReflection(ctx context.Context, id model.MemoryIdentity, revision string, automatic bool) (model.Reflection, error) {
	unlock := s.lockPersonal(id)
	defer unlock()
	rec, err := s.store.GetAgent(ctx, id.Team, id.Name)
	if err != nil && store.KindOf(err) != store.KindNotFound {
		return model.Reflection{}, err
	}
	session, sessionErr := s.store.GetSession(ctx, id.Team, id.Name, id.User)
	if sessionErr != nil && store.KindOf(sessionErr) != store.KindNotFound {
		return model.Reflection{}, sessionErr
	}
	if stopErr := requireReflectionStop(rec, err == nil, session); stopErr != nil {
		return model.Reflection{}, stopErr
	}
	return s.applyCandidate(id, revision, automatic)
}

func requireReflectionStop(rec model.Agent, found bool, session model.Session) error {
	if !found {
		return nil
	}
	if rec.Status == model.AgentRunning || session.KeepAlive && !rec.StopRequested {
		return fmt.Errorf("%w: stop the member and keepalive before applying reflection", memory.ErrConflict)
	}
	if alive, known := proc.Probe(rec); !known || alive {
		return fmt.Errorf("%w: member process has not been confirmed stopped", memory.ErrConflict)
	}
	return nil
}

func (s *Server) applyScheduledReflection(id model.MemoryIdentity, revision string, version uint64, rec model.Agent, found bool, session model.Session) (model.Reflection, error) {
	unlock := s.lockPersonal(id)
	defer unlock()
	// A changed store invalidates the bulk snapshot. Defer to the next scan
	// instead of doing one database query per candidate or trusting stale data.
	if _, current := s.hub.Changed(); current != version {
		return model.Reflection{}, memory.ErrConflict
	}
	if err := requireReflectionStop(rec, found, session); err != nil {
		return model.Reflection{}, err
	}
	return s.applyCandidate(id, revision, true)
}

func (s *Server) applyCandidate(id model.MemoryIdentity, revision string, automatic bool) (model.Reflection, error) {
	files := s.memoryFiles()
	if automatic {
		state, e := files.ReflectionState(id, nil)
		if e != nil {
			return model.Reflection{}, e
		}
		for _, edit := range state.Latest.Edits {
			doc, e := files.Read(id, edit.Path)
			if e != nil && !errors.Is(e, memory.ErrNotFound) {
				return state.Latest, e
			}
			if len(doc.Content) > 1024 && len(edit.Content)*2 < len(doc.Content) {
				return state.Latest, nil
			}
		}
	}
	return files.ApplyReflection(id, revision)
}
