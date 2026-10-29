package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
)

type Options struct {
	DataDir  string
	Socket   string
	HTTPAddr string
	APIKey   string
	Logger   *slog.Logger
}

// Run serves the API on a Unix socket and an optional authenticated HTTP listener.
// A lock file keeps a second daemon from opening the same data directory.
func Run(ctx context.Context, opts Options) error {
	if opts.HTTPAddr != "" && strings.TrimSpace(opts.APIKey) == "" {
		return fmt.Errorf("HTTP listener requires TEAM_WORKER_API_KEY")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return err
	}
	unlock, err := lockDataDir(config.LockPath(opts.DataDir))
	if err != nil {
		return err
	}
	defer unlock()

	st, err := store.Open(config.DBPath(opts.DataDir))
	if err != nil {
		return err
	}
	defer st.Close()
	st.Probe = proc.Probe

	ln, err := listenUnix(opts.Socket)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(opts.Socket)
	var tcp net.Listener
	if opts.HTTPAddr != "" {
		tcp, err = net.Listen("tcp", opts.HTTPAddr)
		if err != nil {
			return fmt.Errorf("listen HTTP: %w", err)
		}
		defer tcp.Close()
	}

	// Cancelling baseCtx ends in-flight long-poll requests so shutdown is prompt.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	handler := New(st, log)
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	servers := []*http.Server{httpSrv}
	listeners := []net.Listener{ln}
	if tcp != nil {
		servers = append(servers, &http.Server{Handler: Authenticate(handler, opts.APIKey), ReadHeaderTimeout: 10 * time.Second,
			BaseContext: func(net.Listener) context.Context { return baseCtx }})
		listeners = append(listeners, tcp)
		log.Info("HTTP API listening", "address", tcp.Addr().String())
	}
	errCh := make(chan error, len(servers))
	var workers sync.WaitGroup
	defer func() {
		cancelBase()
		for _, srv := range servers {
			srv.Close()
		}
		workers.Wait()
	}()
	for i, srv := range servers {
		workers.Go(func() { errCh <- srv.Serve(listeners[i]) })
	}
	workers.Go(func() { reapLoop(baseCtx, st, log) })
	workers.Go(func() { NewNudger(st, log).Run(baseCtx) })
	log.Info("team-workerd started", "socket", opts.Socket, "db", config.DBPath(opts.DataDir), "pid", os.Getpid())

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	log.Info("team-workerd stopping")
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var shutdownErr error
	for _, srv := range servers {
		shutdownErr = errors.Join(shutdownErr, srv.Shutdown(shutdownCtx))
	}
	return shutdownErr
}

// ReapInterval is how often the daemon looks for agents that died without
// running their exit hook, e.g. a killed tmux window.
const ReapInterval = 10 * time.Second

func reapLoop(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			Reap(ctx, st, log)
		}
	}
}

// Reap records an exit for every running agent whose process is gone.
func Reap(ctx context.Context, st *store.Store, log *slog.Logger) {
	agents, err := st.RunningAgents(ctx)
	if err != nil {
		log.Warn("reap: list agents", "err", err)
		return
	}
	for _, a := range agents {
		if alive, known := st.Probe(a); !known || alive {
			continue
		}
		res, err := st.AgentExitedRun(ctx, a.Team, a.Name, a.RunID, -1)
		if err != nil {
			log.Warn("reap: record exit", "team", a.Team, "agent", a.Name, "err", err)
			continue
		}
		log.Info("agent exited", "team", a.Team, "agent", a.Name, "released", len(res.Released))
	}
}

func lockDataDir(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another team-workerd is already using %s", filepath.Dir(path))
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// listenUnix removes a socket left behind by a crashed daemon; the data-dir
// lock already guarantees no live daemon owns it.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
