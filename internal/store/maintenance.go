package store

import (
	"context"
	"database/sql"
	"time"
)

const (
	// DefaultEventRetention is how long old events are kept before pruning.
	DefaultEventRetention = 30 * 24 * time.Hour
	// DefaultKeepEventsPerTeam ensures recent team event history is always preserved.
	DefaultKeepEventsPerTeam = 1000
	// DefaultKeepVikunjaPerTeam bounds delivered Vikunja webhook records per team.
	DefaultKeepVikunjaPerTeam = 1000
	// DefaultKeepIdempotencyPerTeam bounds message idempotency keys per team.
	DefaultKeepIdempotencyPerTeam = 5000
)

// MaintenanceStats reports how many obsolete rows Maintain removed.
type MaintenanceStats struct {
	EventsDeleted      int64
	VikunjaDeleted     int64
	IdempotencyDeleted int64
}

// Total returns the total number of rows removed across all tables.
func (m MaintenanceStats) Total() int64 {
	return m.EventsDeleted + m.VikunjaDeleted + m.IdempotencyDeleted
}

// Maintain prunes aged events, delivered Vikunja webhook records, and excess
// message idempotency keys, then checkpoints the SQLite WAL and optimizes
// planner statistics. It intentionally does not fire OnChange so periodic
// cleanup never wakes idle long-poll watchers.
func (s *Store) Maintain(ctx context.Context, retention time.Duration) (MaintenanceStats, error) {
	if retention <= 0 {
		retention = DefaultEventRetention
	}
	cutoff := s.Now().UTC().Add(-retention).Format("2006-01-02T15:04:05.000000-07:00")
	var stats MaintenanceStats

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	teams, err := listTeamNames(ctx, tx)
	if err != nil {
		return stats, err
	}
	var minCursor sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT MIN(CAST(cursor AS INTEGER)) FROM chat_cursors WHERE cursor GLOB '[0-9]*' AND cursor != ''",
	).Scan(&minCursor); err != nil {
		return stats, err
	}

	for _, team := range teams {
		var res sql.Result
		if minCursor.Valid {
			res, err = tx.ExecContext(ctx,
				`DELETE FROM events WHERE team = ? AND created_at < ? AND id < ?
				 AND id NOT IN (SELECT id FROM events WHERE team = ? ORDER BY id DESC LIMIT ?)`,
				team, cutoff, minCursor.Int64, team, DefaultKeepEventsPerTeam)
		} else {
			res, err = tx.ExecContext(ctx,
				`DELETE FROM events WHERE team = ? AND created_at < ?
				 AND id NOT IN (SELECT id FROM events WHERE team = ? ORDER BY id DESC LIMIT ?)`,
				team, cutoff, team, DefaultKeepEventsPerTeam)
		}
		if err != nil {
			return stats, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return stats, err
		}
		stats.EventsDeleted += n

		res, err = tx.ExecContext(ctx,
			`DELETE FROM vikunja_hooks WHERE team = ? AND delivered = 1
			 AND id NOT IN (SELECT id FROM vikunja_hooks WHERE team = ? AND delivered = 1 ORDER BY id DESC LIMIT ?)`,
			team, team, DefaultKeepVikunjaPerTeam)
		if err != nil {
			return stats, err
		}
		if n, err = res.RowsAffected(); err != nil {
			return stats, err
		}
		stats.VikunjaDeleted += n

		res, err = tx.ExecContext(ctx,
			`DELETE FROM message_idempotency WHERE team = ?
			 AND rowid NOT IN (SELECT rowid FROM message_idempotency WHERE team = ? ORDER BY rowid DESC LIMIT ?)`,
			team, team, DefaultKeepIdempotencyPerTeam)
		if err != nil {
			return stats, err
		}
		if n, err = res.RowsAffected(); err != nil {
			return stats, err
		}
		stats.IdempotencyDeleted += n
	}

	if err := tx.Commit(); err != nil {
		return stats, err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return stats, err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return stats, err
	}
	return stats, nil
}

func listTeamNames(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM teams")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		teams = append(teams, name)
	}
	return teams, rows.Err()
}
