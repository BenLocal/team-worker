package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

// snapshotTables are the per-team tables a snapshot saves, in the order a
// restore inserts them (parents first). Agent processes, worktrees and
// harness runs are not saved: they describe processes and git state that a
// restore cannot bring back.
var snapshotTables = []string{"members", "tasks", "task_deps", "messages", "events", "sessions", "plans", "cost_events"}

// snapshotBundle is the saved state: the teams row and the team's rows of
// each snapshot table, keyed by column name.
type snapshotBundle struct {
	Version int                         `json:"version"`
	Team    map[string]any              `json:"team"`
	Tables  map[string][]map[string]any `json:"tables"`
}

var unsafeTag = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeSnapshotTag turns a tag into an ID suffix like ClawTeam does.
func safeSnapshotTag(tag string) string {
	safe := strings.Trim(unsafeTag.ReplaceAllString(tag, "-"), "-._")
	if safe == "" {
		return "snapshot"
	}
	return safe
}

// CreateSnapshot saves the team's configuration, members, tasks, unread
// messages, events, sessions, plans and costs. The rows are read in the
// same transaction that stores them, so the snapshot is consistent while
// agents keep working.
func (s *Store) CreateSnapshot(ctx context.Context, team, tag string) (model.SnapshotMeta, error) {
	if err := validate(team, "team name"); err != nil {
		return model.SnapshotMeta{}, err
	}
	var meta model.SnapshotMeta
	err := s.write(ctx, func(tx *sql.Tx) error {
		teamRows, err := dumpRows(ctx, tx, "teams", "name", team)
		if err != nil {
			return err
		}
		if len(teamRows) == 0 {
			return notFoundf("Team '%s' not found", team)
		}
		b := snapshotBundle{Version: 1, Team: teamRows[0], Tables: map[string][]map[string]any{}}
		for _, table := range snapshotTables {
			if b.Tables[table], err = dumpRows(ctx, tx, table, "team", team); err != nil {
				return err
			}
		}
		now := s.Now().UTC()
		base := now.Format("20060102T150405")
		if tag != "" {
			base += "-" + safeSnapshotTag(tag)
		}
		id := base
		for n := 2; ; n++ {
			var exists int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM snapshots WHERE team = ? AND id = ?`, team, id).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			id = fmt.Sprintf("%s-%d", base, n)
		}
		meta = model.SnapshotMeta{
			ID: id, TeamName: team, Tag: tag, CreatedAt: s.timestamp(),
			MemberCount: len(b.Tables["members"]), TaskCount: len(b.Tables["tasks"]),
			EventCount: len(b.Tables["events"]), SessionCount: len(b.Tables["sessions"]),
			CostEventCount: len(b.Tables["cost_events"]), MessageCount: len(b.Tables["messages"]),
			PlanCount: len(b.Tables["plans"]),
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		bundleJSON, err := json.Marshal(b)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO snapshots (team, id, tag, created_at, meta, bundle) VALUES (?, ?, ?, ?, ?, ?)`,
			team, id, tag, meta.CreatedAt, string(metaJSON), string(bundleJSON))
		return err
	})
	return meta, err
}

// ListSnapshots returns a team's snapshots, newest first. Snapshots of a
// team that has since been deleted are still listed.
func (s *Store) ListSnapshots(ctx context.Context, team string) ([]model.SnapshotMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT meta FROM snapshots WHERE team = ? ORDER BY created_at DESC, id DESC`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SnapshotMeta{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m model.SnapshotMeta
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("snapshot meta: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteSnapshot removes one snapshot.
func (s *Store) DeleteSnapshot(ctx context.Context, team, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE team = ? AND id = ?`, team, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return notFoundf("Snapshot '%s' not found", id)
		}
		return nil
	})
}

// RestoreSnapshot replaces the team's saved state with the snapshot's, in
// one transaction; a deleted team is created again. It refuses while any of
// the team's agents runs, as a running agent would act on tasks and
// messages that the restore replaces. With dryRun it only reports counts.
func (s *Store) RestoreSnapshot(ctx context.Context, team, id string, dryRun bool) (model.RestoreSummary, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT bundle FROM snapshots WHERE team = ? AND id = ?`, team, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RestoreSummary{}, notFoundf("Snapshot '%s' not found", id)
	}
	if err != nil {
		return model.RestoreSummary{}, err
	}
	var b snapshotBundle
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		return model.RestoreSummary{}, fmt.Errorf("snapshot %s: %w", id, err)
	}
	sum := model.RestoreSummary{
		SnapshotID: id, DryRun: dryRun, Config: b.Team != nil,
		Tasks: len(b.Tables["tasks"]), Events: len(b.Tables["events"]), Sessions: len(b.Tables["sessions"]),
		Costs: len(b.Tables["cost_events"]), Inboxes: len(b.Tables["messages"]), Plans: len(b.Tables["plans"]),
		Members: len(b.Tables["members"]),
	}
	if dryRun {
		return sum, nil
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		running, err := runningAgents(ctx, tx, team)
		if err != nil {
			return err
		}
		if len(running) > 0 {
			return conflictf("Team '%s' has running agents (%s); stop them with `agent stop` before restoring",
				team, strings.Join(running, ", "))
		}
		if err := restoreTeamRow(ctx, tx, team, b.Team); err != nil {
			return err
		}
		for _, table := range slices.Backward(snapshotTables) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE team = ?`, team); err != nil {
				return err
			}
		}
		for _, table := range snapshotTables {
			if err := insertRows(ctx, tx, table, team, b.Tables[table]); err != nil {
				return fmt.Errorf("restore %s: %w", table, err)
			}
		}
		return nil
	})
	return sum, err
}

func runningAgents(ctx context.Context, q queryer, team string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM agents WHERE team = ? AND status = ? ORDER BY name`,
		team, model.AgentRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// restoreTeamRow updates the teams row in place, or inserts it for a
// deleted team. Deleting it would cascade to agents, worktrees and harness
// runs, which a snapshot does not hold.
func restoreTeamRow(ctx context.Context, tx *sql.Tx, team string, row map[string]any) error {
	if row == nil {
		return invalidf("snapshot has no team configuration")
	}
	row = maps.Clone(row)
	row["name"] = team
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE name = ?`, team).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return insertRows(ctx, tx, "teams", "", []map[string]any{row})
	}
	if err != nil {
		return err
	}
	cols, err := tableColumns(ctx, tx, "teams")
	if err != nil {
		return err
	}
	var sets []string
	var args []any
	for _, c := range cols {
		v, ok := row[c]
		if !ok || c == "name" {
			continue
		}
		sets = append(sets, quoteIdent(c)+" = ?")
		args = append(args, sqlValue(v))
	}
	if len(sets) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE teams SET `+strings.Join(sets, ", ")+` WHERE name = ?`, append(args, team)...)
	return err
}

// dumpRows reads every row of table whose column key equals value, in
// insertion order, as column-name maps. table and key are constants.
func dumpRows(ctx context.Context, q queryer, table, key, value string) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT * FROM `+table+` WHERE `+key+` = ? ORDER BY rowid`, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// insertRows inserts saved rows into table with the columns that both the
// row and the current schema have, so snapshots taken before a migration
// still restore; missing columns take their defaults. A non-empty team
// replaces the rows' team column.
func insertRows(ctx context.Context, tx *sql.Tx, table, team string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	cols, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var names, marks []string
		var args []any
		for _, c := range cols {
			v, ok := row[c]
			if c == "team" && team != "" {
				v, ok = team, true
			}
			if !ok {
				continue
			}
			names = append(names, quoteIdent(c))
			marks = append(marks, "?")
			args = append(args, sqlValue(v))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (`+strings.Join(names, ", ")+`) VALUES (`+
			strings.Join(marks, ", ")+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(ctx context.Context, q queryer, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no columns", table)
	}
	return cols, nil
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// sqlValue turns a JSON-decoded value back into a SQL argument: numbers
// become int64 when integral, else float64.
func sqlValue(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	return f
}
