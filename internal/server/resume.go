package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/sysproc"
)

// Resumer resumes an agent's saved session on the daemon's machine by
// running `team-worker agent resume --replace` there, so an operator
// elsewhere gets what running it in the deployment gives.
type Resumer struct {
	// Bin is the team-worker CLI and Env its environment: the daemon's own,
	// reaching this daemon over its socket.
	Bin string
	Env []string
	// Timeout bounds one resume; it stays below the remote client's timeout.
	Timeout time.Duration
	// Run executes the CLI and returns its stdout and exit code; tests
	// replace it.
	Run func(ctx context.Context, bin string, args, env []string) ([]byte, int, error)

	mu   sync.Mutex
	busy map[string]bool
}

// errResumeBusy reports a resume of the same agent that has not finished.
var errResumeBusy = errors.New("busy")

// NewResumer builds a Resumer for the daemon serving dataDir on socket. The
// CLI is the team-worker next to this executable, or else the one in PATH.
func NewResumer(dataDir, socket string) *Resumer {
	bin := "team-worker"
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), "team-worker"); fileExists(p) {
			bin = p
		}
	}
	// The resumed agent gets its identity from spawn; the daemon's own
	// identity and any remote endpoint would point the CLI elsewhere.
	drop := map[string]bool{"TEAM_WORKER_SERVER_URL": true, "TEAM_WORKER_API_KEY": true, "TEAM_WORKER_SOCKET": true,
		"TEAM_WORKER_DATA_DIR": true, "TEAM_WORKER_USER": true, "TEAM_WORKER_AGENT_ID": true, "TEAM_WORKER_AGENT_NAME": true,
		"TEAM_WORKER_AGENT_TYPE": true, "TEAM_WORKER_AGENT_LEADER": true, "TEAM_WORKER_TEAM_NAME": true,
		"TEAM_WORKER_RUN_ID": true, "TEAM_WORKER_PLAN_MODE_REQUIRED": true, "TEAM_WORKER_NO_AUTOSTART": true}
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !drop[name] {
			env = append(env, kv)
		}
	}
	env = append(env, "TEAM_WORKER_DATA_DIR="+dataDir, "TEAM_WORKER_SOCKET="+socket, "TEAM_WORKER_NO_AUTOSTART=1")
	return &Resumer{Bin: bin, Env: env, Timeout: 60 * time.Second, Run: runCLI, busy: map[string]bool{}}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// runCLI runs the CLI in its own session, so it is not caught by signals
// meant for the daemon.
func runCLI(ctx context.Context, bin string, args, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.SysProcAttr = sysproc.Detached()
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), 0, err
}

// resume runs `agent resume` for one agent at a time and returns the CLI's
// JSON output, or its error message with the exit code.
func (r *Resumer) resume(ctx context.Context, team, name, user string, in model.AgentResume) ([]byte, int, error) {
	key := team + "/" + name
	r.mu.Lock()
	if r.busy[key] {
		r.mu.Unlock()
		return nil, 0, errResumeBusy
	}
	r.busy[key] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.busy, key)
		r.mu.Unlock()
	}()
	args := []string{"--json", "agent", "resume", team, name, "--replace"}
	if in.KeepAlive != nil {
		args = append(args, "--keepalive="+strconv.FormatBool(*in.KeepAlive))
	}
	if in.MaxRestarts != nil {
		args = append(args, "--max-restarts", strconv.Itoa(*in.MaxRestarts))
	}
	if in.RestartDelay != "" {
		args = append(args, "--restart-delay", in.RestartDelay)
	}
	if in.DryRun {
		args = append(args, "--dry-run")
	}
	env := r.Env
	if user != "" {
		env = append(append([]string(nil), env...), "TEAM_WORKER_USER="+user)
	}
	// A client that gives up must not cut a resume short halfway.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.Timeout)
	defer cancel()
	out, code, err := r.Run(ctx, r.Bin, args, env)
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	return out, code, err
}

func (s *Server) resumeAgent(w http.ResponseWriter, r *http.Request) {
	local, _ := r.Context().Value(localAccess{}).(bool)
	if s.resumer == nil || (!local && !s.remoteResume) {
		writeError(w, http.StatusForbidden, "resuming agents over HTTP is off; start team-workerd with --remote-resume "+
			"(env TEAM_WORKER_REMOTE_RESUME=1) or run team-worker agent resume on the server")
		return
	}
	var in model.AgentResume
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if in.RestartDelay != "" {
		if _, err := time.ParseDuration(in.RestartDelay); err != nil {
			writeError(w, http.StatusBadRequest, "invalid restartDelay: "+err.Error())
			return
		}
	}
	team, name := r.PathValue("team"), r.PathValue("name")
	a, err := s.store.GetAgent(r.Context(), team, name)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !proc.OnServer(a) {
		writeError(w, http.StatusConflict, fmt.Sprintf("Agent '%s' runs on %s; resume it there", name, proc.Where(a)))
		return
	}
	out, code, err := s.resumer.resume(r.Context(), team, name, r.URL.Query().Get("user"), in)
	switch {
	case errors.Is(err, errResumeBusy):
		writeError(w, http.StatusConflict, fmt.Sprintf("A resume of '%s' is already running", name))
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, fmt.Sprintf("Resuming '%s' took too long; check team-worker agent list", name))
	case err != nil:
		writeError(w, http.StatusInternalServerError, "run team-worker agent resume: "+err.Error())
	case code != 0:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(out, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("team-worker agent resume exited with code %d", code)
		}
		writeError(w, http.StatusConflict, e.Error)
	default:
		s.log.Info("resumed agent", "team", team, "agent", name, "dry_run", in.DryRun, "local", local)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	}
}
