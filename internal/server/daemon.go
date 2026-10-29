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
	"syscall"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
)

type Options struct {
	DataDir string
	Socket  string
	Logger  *slog.Logger
}

// Run opens the database and serves the API on a Unix socket until ctx ends.
// A lock file keeps a second daemon from opening the same data directory.
func Run(ctx context.Context, opts Options) error {
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
	defer os.Remove(opts.Socket)

	// Cancelling baseCtx ends in-flight long-poll requests so shutdown is prompt.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv := &http.Server{
		Handler:           New(st),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	go reapLoop(baseCtx, st, log)
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
	return httpSrv.Shutdown(shutdownCtx)
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
		res, err := st.AgentExited(ctx, a.Team, a.Name)
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
