package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/benlocal/team-worker/internal/model"
)

func (s *Store) SaveSession(ctx context.Context, session model.Session) (model.Session, error) {
	if err := validate(session.Agent, "agent name"); err != nil {
		return session, err
	}
	if err := model.ValidateIdentifier(session.User, "user", true); err != nil {
		return session, invalidf("%s", err)
	}
	if len(session.SessionID) > 1024 || session.Client == "" || session.MaxRestarts < 0 || session.RestartDelayMillis < 0 || session.RestartDelayMillis > 60000 {
		return session, invalidf("invalid session id or client")
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, session.Team); err != nil {
			return err
		}
		a, err := getAgent(ctx, tx, session.Team, session.Agent)
		if err != nil {
			return err
		}
		if a.RunID != session.RunID {
			return conflictf("Session belongs to a different agent run")
		}
		session.UpdatedAt = s.timestamp()
		data, err := json.Marshal(session)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions(team, agent, user_name, data) VALUES (?, ?, ?, ?)
			ON CONFLICT(team, agent, user_name) DO UPDATE SET data = excluded.data`, session.Team, session.Agent, session.User, string(data))
		return err
	})
	return session, err
}

func (s *Store) GetSession(ctx context.Context, team, name, user string) (model.Session, error) {
	var out model.Session
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT data FROM sessions WHERE team = ? AND agent = ? AND user_name = ?", team, name, user).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFoundf("No saved session for '%s'", name)
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(data), &out)
	return out, err
}

func (s *Store) RequestAgentStop(ctx context.Context, team, name, runID string) (model.Agent, error) {
	var out model.Agent
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = getAgent(ctx, tx, team, name)
		if err != nil {
			return err
		}
		if out.RunID != runID {
			return conflictf("Agent run has changed")
		}
		_, err = tx.ExecContext(ctx, "UPDATE agents SET stop_requested = 1 WHERE team = ? AND name = ?", team, name)
		out.StopRequested = true
		return err
	})
	return out, err
}

// StopTeamAgents records all stop intentions in one transaction before cleanup.
func (s *Store) StopTeamAgents(ctx context.Context, team string) ([]model.Agent, error) {
	agents := []model.Agent{}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+agentColumns+" FROM agents WHERE team = ?", team)
		if err != nil {
			return err
		}
		for rows.Next() {
			a, scanErr := scanAgent(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			agents = append(agents, a)
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = tx.ExecContext(ctx, "UPDATE agents SET stop_requested = 1 WHERE team = ?", team)
		return err
	})
	return agents, err
}
