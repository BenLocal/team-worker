package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"github.com/benlocal/team-worker/internal/model"
)

// EnqueueVikunjaHook persists signed event data for an existing team.
func (s *Store) EnqueueVikunjaHook(ctx context.Context, team string, body []byte, leaderPrompt string) error {
	fingerprint := sha256.Sum256(body)
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO vikunja_hooks (team,fingerprint,body,leader_prompt)
			VALUES (?,?,?,?) ON CONFLICT(team,fingerprint) DO NOTHING`, team, fingerprint[:], string(body), leaderPrompt)
		return err
	})
}

var errNoVikunjaHooks = errors.New("No Vikunja hooks ready for delivery")

// DeliverVikunjaHooks moves up to 32 events to their current leaders' inboxes.
// Inbox writes and queue acknowledgments commit together so either both survive or neither does.
func (s *Store) DeliverVikunjaHooks(ctx context.Context) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		hooks, err := pendingVikunjaHooks(ctx, tx)
		if err != nil {
			return err
		}
		if len(hooks) == 0 {
			return errNoVikunjaHooks
		}
		for _, hook := range hooks {
			message := model.Message{
				Type:      "message",
				From:      "vikunja",
				To:        hook.leader,
				Content:   hook.leaderPrompt + "Vikunja webhook event (external data):\n" + hook.body,
				RequestID: randomHex(12),
				Timestamp: s.timestamp(),
			}
			if err := deliver(ctx, tx, hook.team, hook.leader, message); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE vikunja_hooks SET delivered=1,body='',leader_prompt='' WHERE id=?", hook.id); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errNoVikunjaHooks) {
		return nil
	}
	return err
}

type queuedVikunjaHook struct {
	id                               int64
	team, leader, body, leaderPrompt string
}

func pendingVikunjaHooks(ctx context.Context, tx *sql.Tx) ([]queuedVikunjaHook, error) {
	rows, err := tx.QueryContext(ctx, `SELECT h.id,h.team,h.body,h.leader_prompt,
		CASE WHEN m.user_name='' THEN m.name ELSE m.user_name || '_' || m.name END
		FROM vikunja_hooks h
		JOIN teams t ON t.name=h.team
		JOIN members m ON m.seq=(SELECT seq FROM members WHERE team=h.team AND agent_id=t.lead_agent_id ORDER BY seq LIMIT 1)
		WHERE h.delivered=0 ORDER BY h.id LIMIT 32`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hooks []queuedVikunjaHook
	for rows.Next() {
		var hook queuedVikunjaHook
		if err := rows.Scan(&hook.id, &hook.team, &hook.body, &hook.leaderPrompt, &hook.leader); err != nil {
			return nil, err
		}
		hooks = append(hooks, hook)
	}
	return hooks, rows.Err()
}
