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

	"github.com/benlocal/team-worker/internal/board"
	"github.com/benlocal/team-worker/internal/board/live"
	"github.com/benlocal/team-worker/internal/chat"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/runner"
	"github.com/benlocal/team-worker/internal/store"
)

type Options struct {
	DataDir  string
	Socket   string
	HTTPAddr string
	APIKey   string
	Logger   *slog.Logger
	// Board also serves the web board on HTTPAddr: /v1/ stays the API,
	// every other path is the board behind the same access key.
	Board      bool
	BoardLinks []board.Link
	// WebBasePath is the public path prefix a reverse proxy serves the
	// board under, e.g. /team-worker; empty at the root.
	WebBasePath string
}

// Run serves the API on a Unix socket and an optional authenticated HTTP listener.
// A lock file keeps a second daemon from opening the same data directory.
func Run(ctx context.Context, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.Board && opts.HTTPAddr == "" {
		return fmt.Errorf("the web board needs an HTTP listen address (--listen)")
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
	if err := os.Chmod(config.DBPath(opts.DataDir), 0o600); err != nil {
		return fmt.Errorf("protect database: %w", err)
	}
	accessKey, err := st.EnsureAccessKey(ctx, opts.APIKey)
	if err != nil {
		return fmt.Errorf("prepare access key: %w", err)
	}
	if err := st.PublishProtocol(ctx, runner.CoordinationVersion()); err != nil {
		return fmt.Errorf("publish coordination protocol: %w", err)
	}
	// Every agent is started by this daemon, so its process is probed here.
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
	handler.SetRunner(opts.DataDir, "")
	defer handler.Close()
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return context.WithValue(baseCtx, localAccess{}, true) },
	}
	servers := []*http.Server{httpSrv}
	listeners := []net.Listener{ln}
	if tcp != nil {
		api := Authenticate(handler, accessKey)
		web := http.NewServeMux()
		web.HandleFunc(vikunjaHookRoute, handler.vikunjaHook)
		if opts.Board {
			// The daemon's board runs where the agents run, so it has live terminals.
			local := LocalClient(handler)
			site, err := board.Site(local, accessKey, opts.WebBasePath, opts.BoardLinks, live.Handler(local))
			if err != nil {
				return err
			}
			web.Handle("/v1/", api)
			web.Handle("/", site)
		} else {
			web.Handle("/", api)
		}
		servers = append(servers, &http.Server{Handler: web, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
			BaseContext: func(net.Listener) context.Context { return baseCtx }})
		listeners = append(listeners, tcp)
		log.Info("HTTP API listening", "address", tcp.Addr().String(), "board", opts.Board)
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
	// Resume what the last shutdown left running before the first reap
	// records those agents as exited.
	workers.Go(func() {
		if _, err := handler.Restore(baseCtx); err != nil {
			log.Warn("agent restore", "err", err)
		}
		reapLoop(baseCtx, st, log)
	})
	workers.Go(func() { handler.runMemory(baseCtx) })
	workers.Go(func() { runCheckpoints(baseCtx, st, log) })
	workers.Go(func() { runVikunjaHooks(baseCtx, st, log) })
	workers.Go(func() { runMaintenance(baseCtx, st, log) })
	workers.Go(func() { runCostSync(baseCtx, handler, st, log) })
	workers.Go(func() { NewNudger(st, log).Run(baseCtx) })
	workers.Go(func() {
		ann := NewAnnouncer(st, log)
		ann.Hub = handler.hub
		ann.Run(baseCtx)
	})
	workers.Go(func() { NewDeliverer(st, handler.hub, log).Run(baseCtx) })
	workers.Go(func() { NewHookRunner(st, handler.hub, log).Run(baseCtx) })
	workers.Go(func() { NewAutoReviewer(st, handler.hub, log).Run(baseCtx) })
	workers.Go(func() {
		boardURL := strings.TrimRight(os.Getenv("TEAM_WORKER_WEB_URL"), "/")
		if boardURL != "" {
			boardURL += opts.WebBasePath + "/"
		}
		chat.RunFeishu(baseCtx, st, log, opts.DataDir, opts.Socket, boardURL)
	})
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

// MaintenanceInterval is how often the daemon prunes aged database records and
// checkpoints the SQLite WAL.
const MaintenanceInterval = 6 * time.Hour

// CostSyncInterval is how often the daemon syncs session token usage for
// running agents so budgets are enforced even when agents stay alive long-term.
const CostSyncInterval = 5 * time.Minute

func runMaintenance(ctx context.Context, st *store.Store, log *slog.Logger) {
	initial := time.NewTimer(time.Minute)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}
	run := func() {
		stats, err := st.Maintain(ctx, store.DefaultEventRetention)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("store maintenance", "err", err)
			}
			return
		}
		if stats.Total() > 0 {
			log.Info("store maintenance", "events_deleted", stats.EventsDeleted,
				"vikunja_deleted", stats.VikunjaDeleted, "idempotency_deleted", stats.IdempotencyDeleted)
		}
	}
	run()
	ticker := time.NewTicker(MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func runCostSync(ctx context.Context, handler *Server, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(CostSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			agents, err := st.RunningAgents(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("cost sync: list running agents", "err", err)
				}
				continue
			}
			seen := map[string]bool{}
			for _, a := range agents {
				if seen[a.Team] {
					continue
				}
				seen[a.Team] = true
				if _, err := handler.syncTeamCosts(ctx, a.Team, ""); err != nil && ctx.Err() == nil {
					log.Debug("cost sync", "team", a.Team, "err", err)
				}
			}
		}
	}
}

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
		res, err := st.AgentExitedRun(ctx, a.Team, a.Name, a.RunID, store.InterruptedExit)
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
