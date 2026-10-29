// Package store keeps all team-worker state in one SQLite database.
// Only team-workerd opens it, so every write goes through a single connection.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/benlocal/team-worker/internal/model"
)

// Kind classifies store errors so the server can map them to HTTP status codes.
type Kind int

const (
	KindInvalid Kind = iota + 1
	KindNotFound
	KindConflict
)

type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func invalidf(format string, args ...any) error {
	return &Error{Kind: KindInvalid, Msg: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &Error{Kind: KindNotFound, Msg: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &Error{Kind: KindConflict, Msg: fmt.Sprintf(format, args...)}
}

// KindOf returns the error kind, or 0 for unexpected errors.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// ProbeFunc checks a running agent's process. known=false means the answer
// is unavailable, which keeps task locks in place. It runs inside write
// transactions, so it must not touch the store.
type ProbeFunc func(a model.Agent) (alive, known bool)

type Store struct {
	db *sql.DB
	// Now returns the current time; tests replace it.
	Now func() time.Time
	// OnChange runs after every committed write.
	OnChange func()
	// Probe decides whether a task lock held by a running agent may be taken over.
	Probe ProbeFunc
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{
		db:    db,
		Now:   time.Now,
		Probe: func(model.Agent) (bool, bool) { return false, false },
	}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE teams (
		name          TEXT PRIMARY KEY,
		description   TEXT NOT NULL DEFAULT '',
		lead_agent_id TEXT NOT NULL DEFAULT '',
		prompt        TEXT NOT NULL DEFAULT '',
		created_at    TEXT NOT NULL
	);
	CREATE TABLE members (
		seq        INTEGER PRIMARY KEY AUTOINCREMENT,
		team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
		name       TEXT NOT NULL,
		user_name  TEXT NOT NULL DEFAULT '',
		agent_id   TEXT NOT NULL,
		agent_type TEXT NOT NULL,
		joined_at  TEXT NOT NULL,
		UNIQUE (team, name, user_name)
	);
	CREATE TABLE tasks (
		team        TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
		id          TEXT NOT NULL,
		subject     TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL,
		priority    TEXT NOT NULL,
		owner       TEXT NOT NULL DEFAULT '',
		locked_by   TEXT NOT NULL DEFAULT '',
		locked_at   TEXT NOT NULL DEFAULT '',
		blocks      TEXT NOT NULL DEFAULT '[]',
		started_at  TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL,
		metadata    TEXT NOT NULL DEFAULT '{}',
		PRIMARY KEY (team, id)
	);
	CREATE INDEX tasks_owner_status ON tasks(team, owner, status);
	CREATE TABLE task_deps (
		seq        INTEGER PRIMARY KEY AUTOINCREMENT,
		team       TEXT NOT NULL,
		task_id    TEXT NOT NULL,
		blocked_by TEXT NOT NULL,
		UNIQUE (team, task_id, blocked_by),
		FOREIGN KEY (team, task_id) REFERENCES tasks(team, id) ON DELETE CASCADE
	);
	CREATE INDEX task_deps_blocked_by ON task_deps(team, blocked_by);
	CREATE TABLE messages (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
		inbox      TEXT NOT NULL,
		body       TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX messages_inbox ON messages(team, inbox, id);
	CREATE TABLE events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
		kind       TEXT NOT NULL,
		body       TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX events_team_kind ON events(team, kind, id);`,

	`CREATE TABLE agents (
		team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
		name       TEXT NOT NULL,
		agent_id   TEXT NOT NULL,
		backend    TEXT NOT NULL,
		target     TEXT NOT NULL DEFAULT '',
		pid        INTEGER NOT NULL DEFAULT 0,
		command    TEXT NOT NULL DEFAULT '[]',
		cwd        TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL,
		started_at TEXT NOT NULL,
		exited_at  TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (team, name)
	);
	CREATE INDEX agents_status ON agents(status);`,
}

// migrate applies pending migrations, tracking progress in PRAGMA user_version.
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// write runs fn in a transaction and fires OnChange after a successful commit.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.OnChange != nil {
		s.OnChange()
	}
	return nil
}

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// timestamp matches Python's datetime.isoformat() for UTC, e.g. 2026-10-29T05:00:00.123456+00:00.
func (s *Store) timestamp() string {
	return s.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)[:n]
}

// NewAgentID returns a 12-character agent id, the format ClawTeam uses.
func NewAgentID() string { return randomHex(12) }

func requireTeam(ctx context.Context, q queryer, team string) error {
	if err := validate(team, "team name"); err != nil {
		return err
	}
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM teams WHERE name = ?", team).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notFoundf("Team '%s' not found", team)
	}
	return err
}
