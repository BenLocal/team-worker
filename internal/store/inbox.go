package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/benlocal/team-worker/internal/model"
)

type SendInput struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Content string            `json:"content"`
	Type    model.MessageType `json:"type"`
	Key     string            `json:"key"`
}

// Send delivers a message to one inbox and records it in the team event log.
func (s *Store) Send(ctx context.Context, team string, in SendInput) (model.Message, error) {
	if in.Type == "" {
		in.Type = "message"
	}
	if _, err := model.ParseMessageType(string(in.Type)); err != nil {
		return model.Message{}, invalidf("%s", err)
	}
	msg := model.Message{
		Type:      in.Type,
		From:      in.From,
		To:        in.To,
		Content:   in.Content,
		RequestID: randomHex(12),
		Timestamp: s.timestamp(),
		Key:       in.Key,
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		inbox, err := resolveInbox(ctx, tx, team, in.To, "")
		if err != nil {
			return err
		}
		return deliver(ctx, tx, team, inbox, msg)
	})
	return msg, err
}

type BroadcastInput struct {
	From    string            `json:"from"`
	Content string            `json:"content"`
	Type    model.MessageType `json:"type"`
	Key     string            `json:"key"`
	Exclude []string          `json:"exclude"`
}

// Broadcast sends one message to every member inbox except the sender's.
func (s *Store) Broadcast(ctx context.Context, team string, in BroadcastInput) ([]model.Message, error) {
	if in.Type == "" {
		in.Type = "broadcast"
	}
	if _, err := model.ParseMessageType(string(in.Type)); err != nil {
		return nil, invalidf("%s", err)
	}
	var sent []model.Message
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		members, err := listMembers(ctx, tx, team)
		if err != nil {
			return err
		}
		excluded := map[string]bool{}
		for _, name := range append([]string{in.From}, in.Exclude...) {
			if name == "" {
				continue
			}
			excluded[name] = true
			if inbox, err := resolveInbox(ctx, tx, team, name, ""); err == nil {
				excluded[inbox] = true
			}
		}
		seen := map[string]bool{}
		now := s.timestamp()
		for _, m := range members {
			inbox := m.InboxName()
			if excluded[inbox] || seen[inbox] {
				continue
			}
			seen[inbox] = true
			msg := model.Message{
				Type:      in.Type,
				From:      in.From,
				To:        inbox,
				Content:   in.Content,
				RequestID: randomHex(12),
				Timestamp: now,
				Key:       in.Key,
			}
			if err := deliver(ctx, tx, team, inbox, msg); err != nil {
				return err
			}
			sent = append(sent, msg)
		}
		return nil
	})
	if sent == nil {
		sent = []model.Message{}
	}
	return sent, err
}

func deliver(ctx context.Context, tx *sql.Tx, team, inbox string, msg model.Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO messages (team, inbox, body, created_at) VALUES (?, ?, ?, ?)",
		team, inbox, string(body), msg.Timestamp); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO events (team, kind, body, created_at) VALUES (?, 'message', ?, ?)",
		team, string(body), msg.Timestamp)
	return err
}

// Receive consumes up to limit messages from an agent's inbox, oldest first.
// Claiming and deleting happen in one transaction, so a message is delivered once.
func (s *Store) Receive(ctx context.Context, team, agent, user string, limit int) ([]model.Message, error) {
	if limit <= 0 {
		limit = 10
	}
	var msgs []model.Message
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		inbox, err := resolveInbox(ctx, tx, team, agent, user)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			`DELETE FROM messages WHERE id IN (
			   SELECT id FROM messages WHERE team = ? AND inbox = ? ORDER BY id LIMIT ?
			 ) RETURNING id, body`,
			team, inbox, limit)
		if err != nil {
			return err
		}
		msgs, err = scanMessages(rows)
		return err
	})
	return msgs, err
}

// Peek returns pending messages without consuming them.
func (s *Store) Peek(ctx context.Context, team, agent, user string) ([]model.Message, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	inbox, err := resolveInbox(ctx, s.db, team, agent, user)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, body FROM messages WHERE team = ? AND inbox = ? ORDER BY id", team, inbox)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// MessageLog returns the newest limit sent messages, oldest first,
// optionally only those from one sender.
func (s *Store) MessageLog(ctx context.Context, team string, limit int, from string) ([]model.Message, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	query := "SELECT id, body FROM events WHERE team = ? AND kind = 'message'"
	args := []any{team}
	if from != "" {
		query += " AND json_extract(body, '$.from') = ?"
		args = append(args, from)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// scanMessages decodes (id, body) rows and returns them in id order.
func scanMessages(rows *sql.Rows) ([]model.Message, error) {
	defer rows.Close()
	type row struct {
		id  int64
		msg model.Message
	}
	var items []row
	for rows.Next() {
		var r row
		var body string
		if err := rows.Scan(&r.id, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &r.msg); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	msgs := make([]model.Message, len(items))
	for i, r := range items {
		msgs[i] = r.msg
	}
	return msgs, nil
}
