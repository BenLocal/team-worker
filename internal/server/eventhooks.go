package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// HookTimeout bounds one hook command, as ClawTeam's 30-second limit.
var HookTimeout = 30 * time.Second

// hookOutputLimit caps the output a hook result keeps.
const hookOutputLimit = 4096

// HookRunner runs user event hooks for the events the store records. It
// drains the outbox after every write and deletes an event once its hooks
// ran, so a daemon restart in between runs them again.
type HookRunner struct {
	Store *store.Store
	Hub   *Hub
	Log   *slog.Logger
	// Exec runs one hook; tests replace it.
	Exec func(ctx context.Context, h model.EventHook, payload map[string]any) model.HookResult
}

func NewHookRunner(st *store.Store, hub *Hub, log *slog.Logger) *HookRunner {
	return &HookRunner{Store: st, Hub: hub, Log: log, Exec: RunShellHook}
}

// hookPoll is a fallback for events written while the runner was busy and
// whose wakeup it missed.
const hookPoll = 30 * time.Second

// Run drains the outbox until ctx ends.
func (h *HookRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(hookPoll)
	defer ticker.Stop()
	for {
		// Take the channel before reading so a write in between is not missed.
		changed, _ := h.Hub.Changed()
		if err := h.Drain(ctx); err != nil && ctx.Err() == nil {
			h.Log.Warn("event hooks", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
		}
	}
}

// Drain runs the hooks of every recorded event, oldest first.
func (h *HookRunner) Drain(ctx context.Context) error {
	for {
		events, err := h.Store.PendingEvents(ctx, 50)
		if err != nil || len(events) == 0 {
			return err
		}
		// One read of the hooks per batch; an event recorded for a hook that
		// was removed since then runs nothing.
		hooks, err := h.Store.ListEventHooks(ctx)
		if err != nil {
			return err
		}
		for _, e := range events {
			for _, hook := range hooks {
				if !hook.Matches(e.Type, e.Team) {
					continue
				}
				res := h.Exec(ctx, hook, e.Payload)
				if ctx.Err() != nil {
					// Shutting down: keep the event so the next start runs it.
					return nil
				}
				if res.Error != "" || res.ExitCode != 0 {
					h.Log.Warn("event hook failed", "event", e.Type, "team", e.Team, "hook", hook.ID,
						"exit_code", res.ExitCode, "err", res.Error, "output", res.Output)
				} else {
					h.Log.Debug("event hook ran", "event", e.Type, "team", e.Team, "hook", hook.ID)
				}
			}
			if err := h.Store.EventDone(ctx, e.ID); err != nil {
				return err
			}
		}
	}
}

// RunShellHook runs a hook's command with sh -c. The event goes to the
// command as JSON on stdin and as TEAM_WORKER_EVENT_* variables: TYPE, plus
// one per field, upper-cased, lists joined with commas.
func RunShellHook(ctx context.Context, h model.EventHook, payload map[string]any) model.HookResult {
	res := model.HookResult{HookID: h.ID, Command: h.Command}
	body, err := json.Marshal(payload)
	if err != nil {
		res.ExitCode, res.Error = -1, err.Error()
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, HookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", h.Command)
	cmd.Env = append(os.Environ(), hookEnv(payload)...)
	cmd.Stdin = bytes.NewReader(body)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// A child that keeps the output open must not hold the runner past the timeout.
	cmd.WaitDelay = time.Second
	start := time.Now()
	err = cmd.Run()
	res.DurationMS = time.Since(start).Milliseconds()
	res.Output = out.String()
	if len(res.Output) > hookOutputLimit {
		res.Output = res.Output[:hookOutputLimit]
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		res.ExitCode, res.Error = -1, fmt.Sprintf("timed out after %s", HookTimeout)
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.ExitCode, res.Error = -1, err.Error()
	}
	return res
}

func hookEnv(payload map[string]any) []string {
	env := make([]string, 0, len(payload))
	for k, v := range payload {
		env = append(env, "TEAM_WORKER_EVENT_"+strings.ToUpper(k)+"="+envValue(v))
	}
	return env
}

func envValue(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = envValue(item)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(v)
	}
}

func (s *Server) listEventHooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.store.ListEventHooks(r.Context())
	respond(w, hooks, err)
}

func (s *Server) addEventHook(w http.ResponseWriter, r *http.Request) {
	var in store.EventHookInput
	if !decode(w, r, &in) {
		return
	}
	h, err := s.store.AddEventHook(r.Context(), in)
	if err == nil {
		s.log.Info("event hook added", "id", h.ID, "event", h.Event, "team", h.Team)
	}
	respond(w, h, err)
}

func (s *Server) removeEventHooks(w http.ResponseWriter, r *http.Request) {
	event, command := r.URL.Query().Get("event"), r.URL.Query().Get("command")
	n, err := s.store.RemoveEventHooks(r.Context(), event, command)
	if err == nil && n > 0 {
		s.log.Info("event hooks removed", "event", event, "count", n)
	}
	respond(w, map[string]any{"event": event, "removed": n}, err)
}

// testEventHook runs the hooks of an event type right away with a made-up
// event, as ClawTeam's `hook test`.
func (s *Server) testEventHook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Event string `json:"event"`
		Team  string `json:"team"`
		Agent string `json:"agent"`
	}
	if !decode(w, r, &in) {
		return
	}
	payload, hooks, err := s.store.TestEvent(r.Context(), in.Event, in.Team, in.Agent)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out := model.HookTestResult{Event: in.Event, Payload: payload, Results: []model.HookResult{}}
	for _, h := range hooks {
		out.Results = append(out.Results, RunShellHook(r.Context(), h, payload))
	}
	respond(w, out, nil)
}
