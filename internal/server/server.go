// Package server exposes the store as a JSON API for the team-worker CLI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// MaxWait caps how long a long-poll request may block.
const MaxWait = 60 * time.Second

// Hub wakes long-poll requests whenever the store commits a write and
// counts writes, so clients can ask whether anything changed since a version.
type Hub struct {
	mu      sync.Mutex
	ch      chan struct{}
	version uint64
}

func NewHub() *Hub { return &Hub{ch: make(chan struct{})} }

// Changed returns a channel that closes on the next write, and the current
// version. Take it before reading state so a write in between is not missed.
func (h *Hub) Changed() (<-chan struct{}, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch, h.version
}

func (h *Hub) Notify() {
	h.mu.Lock()
	h.version++
	close(h.ch)
	h.ch = make(chan struct{})
	h.mu.Unlock()
}

type Server struct {
	store *store.Store
	hub   *Hub
	mux   *http.ServeMux
	log   *slog.Logger
}

// New wires the store's change notifications into a hub and registers
// routes. A nil logger discards logs.
func New(s *store.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	srv := &Server{store: s, hub: NewHub(), mux: http.NewServeMux(), log: log}
	s.OnChange = srv.hub.Notify
	srv.routes()
	return srv
}

// SlowRequest is the duration after which a request that is not a long poll
// is logged as slow.
var SlowRequest = 2 * time.Second

// ServeHTTP logs every request: failures and slow requests as warnings
// (server errors as errors), the rest at debug level. Bodies are never logged.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	elapsed := time.Since(start)
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status,
		"ms", elapsed.Milliseconds()}
	if rec.errMsg != "" {
		attrs = append(attrs, "error", rec.errMsg)
	}
	// Long polls wait on purpose; only their failures matter.
	longPoll := r.URL.Query().Get("wait") != ""
	switch {
	case rec.status >= 500:
		s.log.Error("request failed", attrs...)
	case rec.status == http.StatusNotFound && r.Method == http.MethodGet:
		// Existence checks, e.g. spawn looking for a running agent, are routine.
		s.log.Debug("request", attrs...)
	case rec.status >= 400:
		s.log.Warn("request rejected", attrs...)
	case !longPoll && elapsed > SlowRequest:
		s.log.Warn("slow request", attrs...)
	default:
		s.log.Debug("request", attrs...)
	}
}

// statusRecorder keeps the response status and, for errors, the message.
type statusRecorder struct {
	http.ResponseWriter
	status int
	errMsg string
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status >= 400 && r.errMsg == "" {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil {
			r.errMsg = e.Error
		}
	}
	return r.ResponseWriter.Write(b)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /v1/access-key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if allowed, _ := r.Context().Value(localAccess{}).(bool); !allowed {
			writeError(w, http.StatusForbidden, "Access key is available only over the local Unix socket")
			return
		}
		key, err := s.store.AccessKey(r.Context())
		respond(w, map[string]string{"key": key}, err)
	})
	m.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	m.HandleFunc("GET /v1/changes", s.changes)

	m.HandleFunc("GET /v1/teams", s.listTeams)
	m.HandleFunc("POST /v1/teams", s.createTeam)
	m.HandleFunc("GET /v1/teams/{team}", s.getTeam)
	m.HandleFunc("DELETE /v1/teams/{team}", s.deleteTeam)
	m.HandleFunc("POST /v1/teams/{team}/members", s.addMember)
	m.HandleFunc("GET /v1/teams/{team}/leader", s.leader)

	m.HandleFunc("GET /v1/teams/{team}/tasks", s.listTasks)
	m.HandleFunc("POST /v1/teams/{team}/tasks", s.createTask)
	m.HandleFunc("GET /v1/teams/{team}/tasks/stats", s.taskStats)
	m.HandleFunc("GET /v1/teams/{team}/tasks/{id}", s.getTask)
	m.HandleFunc("PATCH /v1/teams/{team}/tasks/{id}", s.updateTask)

	m.HandleFunc("GET /v1/teams/{team}/prompt", s.getPrompt)
	m.HandleFunc("PUT /v1/teams/{team}/prompt", s.setPrompt)
	m.HandleFunc("GET /v1/teams/{team}/members/{name}/prompt", s.getAgentPrompt)
	m.HandleFunc("PUT /v1/teams/{team}/members/{name}/prompt", s.setAgentPrompt)
	m.HandleFunc("GET /v1/teams/{team}/board", s.board)

	m.HandleFunc("GET /v1/teams/{team}/agents", s.listAgents)
	m.HandleFunc("POST /v1/teams/{team}/agents", s.registerAgent)
	m.HandleFunc("POST /v1/teams/{team}/agents/stop", s.stopTeamAgents)
	m.HandleFunc("GET /v1/teams/{team}/agents/{name}", s.getAgent)
	m.HandleFunc("POST /v1/teams/{team}/agents/{name}/exit", s.agentExited)
	m.HandleFunc("POST /v1/teams/{team}/agents/{name}/stop", s.requestAgentStop)
	m.HandleFunc("GET /v1/teams/{team}/sessions/{name}", s.getSession)
	m.HandleFunc("PUT /v1/teams/{team}/sessions/{name}", s.saveSession)

	m.HandleFunc("POST /v1/teams/{team}/messages", s.send)
	m.HandleFunc("POST /v1/teams/{team}/broadcast", s.broadcast)
	m.HandleFunc("GET /v1/teams/{team}/messages/log", s.messageLog)
	m.HandleFunc("GET /v1/teams/{team}/inboxes/{agent}", s.peek)
	m.HandleFunc("POST /v1/teams/{team}/inboxes/{agent}/receive", s.receive)
}

// changes returns the current write version, waiting up to wait for it to
// pass after. Hooks use it to sleep until anything in the store changes.
func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseUint(q.Get("after"), 10, 64)
	var wait time.Duration
	if v := q.Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			writeError(w, http.StatusBadRequest, "invalid wait duration")
			return
		}
		wait = min(d, MaxWait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		changed, version := s.hub.Changed()
		if version > after || wait == 0 {
			respond(w, map[string]uint64{"version": version}, nil)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			respond(w, map[string]uint64{"version": version}, nil)
			return
		case <-r.Context().Done():
			respond(w, nil, r.Context().Err())
			return
		}
	}
}

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := s.store.ListTeams(r.Context())
	respond(w, teams, err)
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	var in store.CreateTeamInput
	if !decode(w, r, &in) {
		return
	}
	team, err := s.store.CreateTeam(r.Context(), in)
	if err == nil {
		s.log.Info("team created", "team", team.Name, "leader", in.LeaderName)
	}
	respond(w, team, err)
}

func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	team, err := s.store.GetTeam(r.Context(), r.PathValue("team"))
	respond(w, team, err)
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.DeleteTeam(r.Context(), r.PathValue("team"))
	if deleted {
		s.log.Info("team deleted", "team", r.PathValue("team"))
	}
	respond(w, map[string]bool{"deleted": deleted}, err)
}

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	var in store.AddMemberInput
	if !decode(w, r, &in) {
		return
	}
	member, err := s.store.AddMember(r.Context(), r.PathValue("team"), in)
	if err == nil {
		s.log.Info("member added", "team", r.PathValue("team"), "member", member.Name, "type", member.AgentType)
	}
	respond(w, member, err)
}

func (s *Server) leader(w http.ResponseWriter, r *http.Request) {
	member, err := s.store.Leader(r.Context(), r.PathValue("team"))
	respond(w, member, err)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskFilter{
		Status:       model.TaskStatus(q.Get("status")),
		Owner:        q.Get("owner"),
		Priority:     model.Priority(q.Get("priority")),
		SortPriority: q.Get("sort") == "priority",
	}
	tasks, err := s.store.ListTasks(r.Context(), r.PathValue("team"), f)
	respond(w, tasks, err)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in store.CreateTaskInput
	if !decode(w, r, &in) {
		return
	}
	task, err := s.store.CreateTask(r.Context(), r.PathValue("team"), in)
	if err == nil {
		s.log.Info("task created", "team", r.PathValue("team"), "task", task.ID, "subject", task.Subject,
			"owner", task.Owner, "status", task.Status, "blockedBy", task.BlockedBy)
	}
	respond(w, task, err)
}

func (s *Server) taskStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.TaskStats(r.Context(), r.PathValue("team"))
	respond(w, stats, err)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.GetTask(r.Context(), r.PathValue("team"), r.PathValue("id"))
	respond(w, task, err)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	var in store.UpdateTaskInput
	if !decode(w, r, &in) {
		return
	}
	team, id := r.PathValue("team"), r.PathValue("id")
	before, _ := s.store.GetTask(r.Context(), team, id)
	task, err := s.store.UpdateTask(r.Context(), team, id, in)
	if err == nil {
		s.logTaskUpdate(team, before, task, in.Caller)
	}
	respond(w, task, err)
}

// logTaskUpdate records what an update changed: status, owner, lock or blockers.
func (s *Server) logTaskUpdate(team string, before, after model.Task, caller string) {
	attrs := []any{"team", team, "task", after.ID, "subject", after.Subject}
	if caller != "" {
		attrs = append(attrs, "by", caller)
	}
	changed := false
	if before.Status != after.Status {
		attrs = append(attrs, "status", string(before.Status)+" -> "+string(after.Status))
		changed = true
	}
	if before.Owner != after.Owner {
		attrs = append(attrs, "owner", before.Owner+" -> "+after.Owner)
		changed = true
	}
	if before.LockedBy != after.LockedBy {
		attrs = append(attrs, "lock", before.LockedBy+" -> "+after.LockedBy)
		changed = true
	}
	if len(before.BlockedBy) != len(after.BlockedBy) {
		attrs = append(attrs, "blockedBy", after.BlockedBy)
		changed = true
	}
	if changed {
		s.log.Info("task updated", attrs...)
	} else {
		s.log.Debug("task updated", attrs...)
	}
}

func (s *Server) getPrompt(w http.ResponseWriter, r *http.Request) {
	prompt, err := s.store.TeamPrompt(r.Context(), r.PathValue("team"))
	respond(w, map[string]string{"prompt": prompt}, err)
}

func (s *Server) getAgentPrompt(w http.ResponseWriter, r *http.Request) {
	prompt, err := s.store.AgentPrompt(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user"))
	respond(w, map[string]string{"prompt": prompt}, err)
}

func (s *Server) setAgentPrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &in) {
		return
	}
	changed, err := s.store.SetAgentPrompt(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("user"), in.Prompt)
	respond(w, map[string]bool{"changed": changed}, err)
}

func (s *Server) setPrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &in) {
		return
	}
	changed, err := s.store.SetTeamPrompt(r.Context(), r.PathValue("team"), in.Prompt)
	if changed {
		s.log.Info("team instructions updated", "team", r.PathValue("team"), "chars", len(in.Prompt))
	}
	respond(w, map[string]bool{"changed": changed}, err)
}

// withAlive fills Alive from a live probe for agents recorded as running.
func (s *Server) withAlive(a model.Agent) model.Agent {
	alive := false
	if a.Status == model.AgentRunning {
		var known bool
		if alive, known = s.store.Probe(a); !known {
			return a
		}
	}
	a.Alive = &alive
	return a
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("messages"))
	if limit <= 0 {
		limit = 50
	}
	b, err := s.store.Board(r.Context(), r.PathValue("team"), limit)
	if err == nil {
		for i := range b.Members {
			if a := b.Members[i].Agent; a != nil {
				probed := s.withAlive(*a)
				b.Members[i].Agent = &probed
			}
		}
	}
	respond(w, b, err)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context(), r.PathValue("team"))
	for i := range agents {
		agents[i] = s.withAlive(agents[i])
	}
	respond(w, agents, err)
}

func (s *Server) registerAgent(w http.ResponseWriter, r *http.Request) {
	var in model.Agent
	if !decode(w, r, &in) {
		return
	}
	in.Team = r.PathValue("team")
	a, err := s.store.RegisterAgent(r.Context(), in)
	if err == nil {
		s.log.Info("agent registered", "team", a.Team, "agent", a.Name, "backend", a.Backend,
			"target", a.Target, "pid", a.PID, "nudge", a.Nudge, "cwd", a.Cwd)
	}
	respond(w, a, err)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAgent(r.Context(), r.PathValue("team"), r.PathValue("name"))
	if err == nil {
		a = s.withAlive(a)
	}
	respond(w, a, err)
}

func (s *Server) agentExited(w http.ResponseWriter, r *http.Request) {
	code := 0
	if value := r.URL.Query().Get("code"); value != "" {
		var err error
		code, err = strconv.Atoi(value)
		if err != nil {
			respond(w, nil, &store.Error{Kind: store.KindInvalid, Msg: "invalid exit code"})
			return
		}
	}
	res, err := s.store.AgentExitedRun(r.Context(), r.PathValue("team"), r.PathValue("name"), r.URL.Query().Get("run_id"), code)
	if err == nil && res.Changed {
		s.log.Info("agent exited", "team", r.PathValue("team"), "agent", res.Agent, "released", len(res.Released))
	}
	respond(w, res, err)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var in store.SendInput
	if !decode(w, r, &in) {
		return
	}
	msg, err := s.store.Send(r.Context(), r.PathValue("team"), in)
	if err == nil {
		s.log.Info("message sent", "team", r.PathValue("team"), "from", msg.From, "to", msg.To,
			"type", msg.Type, "chars", len(msg.Content))
	}
	respond(w, msg, err)
}

func (s *Server) broadcast(w http.ResponseWriter, r *http.Request) {
	var in store.BroadcastInput
	if !decode(w, r, &in) {
		return
	}
	msgs, err := s.store.Broadcast(r.Context(), r.PathValue("team"), in)
	if err == nil {
		s.log.Info("message broadcast", "team", r.PathValue("team"), "from", in.From, "type", in.Type,
			"recipients", len(msgs), "chars", len(in.Content))
	}
	respond(w, msgs, err)
}

func (s *Server) messageLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	msgs, err := s.store.MessageLog(r.Context(), r.PathValue("team"), limit, q.Get("from"))
	respond(w, msgs, err)
}

func (s *Server) peek(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.store.Peek(r.Context(), r.PathValue("team"), r.PathValue("agent"), r.URL.Query().Get("user"))
	respond(w, msgs, err)
}

// receive consumes messages. With wait=<duration> it blocks until a message
// arrives or the wait elapses, so watchers do not need to poll.
func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	team, agent, user := r.PathValue("team"), r.PathValue("agent"), q.Get("user")
	limit, _ := strconv.Atoi(q.Get("limit"))
	var wait time.Duration
	if v := q.Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			writeError(w, http.StatusBadRequest, "invalid wait duration")
			return
		}
		wait = min(d, MaxWait)
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	for {
		changed, _ := s.hub.Changed()
		msgs, err := s.store.Receive(r.Context(), team, agent, user, limit)
		if err != nil || len(msgs) > 0 || wait == 0 {
			respond(w, msgs, err)
			return
		}
		select {
		case <-changed:
		case <-ctx.Done():
			respond(w, []model.Message{}, r.Context().Err())
			return
		}
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, v)
		return
	}
	// Request contexts end when the daemon shuts down; clients retry on 503.
	if errors.Is(err, context.Canceled) {
		writeError(w, http.StatusServiceUnavailable, "team-workerd is shutting down")
		return
	}
	status := http.StatusInternalServerError
	switch store.KindOf(err) {
	case store.KindInvalid:
		status = http.StatusBadRequest
	case store.KindNotFound:
		status = http.StatusNotFound
	case store.KindConflict:
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
