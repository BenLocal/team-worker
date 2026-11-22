package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/benlocal/team-worker/internal/model"
)

type SendInput struct {
	From           string            `json:"from"`
	To             string            `json:"to"`
	Content        string            `json:"content"`
	Type           model.MessageType `json:"type"`
	Key            string            `json:"key"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	ExpectedLeader string            `json:"expectedLeader,omitempty"`
	ChatBindingID  string            `json:"chatBindingId,omitempty"`
	ChatSenderID   string            `json:"chatSenderId,omitempty"`
}

// Send delivers a message to one inbox and records it in the team event log.
func (s *Store) Send(ctx context.Context, team string, in SendInput) (model.Message, error) {
	if in.Type == "" {
		in.Type = "message"
	}
	if _, err := model.ParseMessageType(string(in.Type)); err != nil {
		return model.Message{}, invalidf("%s", err)
	}
	if len(in.IdempotencyKey) > 128 {
		return model.Message{}, invalidf("Idempotency key exceeds 128 bytes")
	}
	if in.ChatBindingID != "" || in.ChatSenderID != "" {
		if in.ChatBindingID == "" || in.ChatSenderID == "" || in.ExpectedLeader == "" || in.Type != "message" {
			return model.Message{}, invalidf("Chat delivery requires a binding, sender and expected leader")
		}
	}
	var request string
	if in.IdempotencyKey != "" {
		body, err := json.Marshal(in)
		if err != nil {
			return model.Message{}, err
		}
		request = fmt.Sprintf("%x", sha256.Sum256(body))
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
		if request != "" {
			var previous, response string
			err := tx.QueryRowContext(ctx, "SELECT request,response FROM message_idempotency WHERE team=? AND key=?", team, in.IdempotencyKey).Scan(&previous, &response)
			if err == nil {
				if previous != request {
					return conflictf("Idempotency key was already used for a different message")
				}
				return json.Unmarshal([]byte(response), &msg)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if in.ChatBindingID != "" {
			if err := checkChatSendPermission(ctx, tx, team, in.ChatBindingID, in.ChatSenderID); err != nil {
				return err
			}
		}
		var inbox string
		var err error
		if in.ExpectedLeader != "" {
			err = tx.QueryRowContext(ctx, `SELECT CASE WHEN m.user_name = '' THEN m.name ELSE m.user_name || '_' || m.name END
				FROM teams t JOIN members m ON m.team=t.name AND m.agent_id=t.lead_agent_id
				WHERE t.name=? AND t.lead_agent_id=?`, team, in.ExpectedLeader).Scan(&inbox)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && inbox != in.To) {
				return conflictf("Team leader changed before delivery")
			}
		} else {
			inbox, err = resolveInbox(ctx, tx, team, in.To, "")
		}
		if err != nil {
			return err
		}
		if err := deliver(ctx, tx, team, inbox, msg); err != nil {
			return err
		}
		if request != "" {
			response, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO message_idempotency (team,key,request,response) VALUES (?,?,?,?)",
				team, in.IdempotencyKey, request, string(response)); err != nil {
				return err
			}
		}
		if msg.Type == "idle" {
			return s.emit(ctx, tx, team, "AgentIdle", map[string]any{"agent_name": msg.From})
		}
		return nil
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

// ReceiveWith consumes up to limit messages like Receive and hands them to
// handle outside the transaction. When handle fails, the messages go back
// into the inbox under their old ids, so they keep their place ahead of
// newer ones, and the error is returned.
func (s *Store) ReceiveWith(ctx context.Context, team, agent, user string, limit int, handle func([]model.Message) error) error {
	if limit <= 0 {
		limit = 10
	}
	type claimed struct {
		id              int64
		body, createdAt string
	}
	var inbox string
	var rows []claimed
	var msgs []model.Message
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		var err error
		if inbox, err = resolveInbox(ctx, tx, team, agent, user); err != nil {
			return err
		}
		result, err := tx.QueryContext(ctx,
			`DELETE FROM messages WHERE id IN (
			   SELECT id FROM messages WHERE team = ? AND inbox = ? ORDER BY id LIMIT ?
			 ) RETURNING id, body, created_at`,
			team, inbox, limit)
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var c claimed
			if err := result.Scan(&c.id, &c.body, &c.createdAt); err != nil {
				return err
			}
			rows = append(rows, c)
		}
		if err := result.Err(); err != nil {
			return err
		}
		// Decoding inside the transaction rolls the claim back on bad data.
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
		msgs = make([]model.Message, len(rows))
		for i, c := range rows {
			if err := json.Unmarshal([]byte(c.body), &msgs[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(rows) == 0 {
		return err
	}
	handleErr := handle(msgs)
	if handleErr == nil {
		return nil
	}
	// Put them back even when the caller is shutting down.
	restoreCtx := context.WithoutCancel(ctx)
	restoreErr := s.write(restoreCtx, func(tx *sql.Tx) error {
		for _, c := range rows {
			if _, err := tx.ExecContext(restoreCtx,
				"INSERT INTO messages (id, team, inbox, body, created_at) VALUES (?, ?, ?, ?, ?)",
				c.id, team, inbox, c.body, c.createdAt); err != nil {
				return err
			}
		}
		return nil
	})
	if restoreErr != nil {
		return errors.Join(handleErr, fmt.Errorf("return messages to the inbox: %w", restoreErr))
	}
	return handleErr
}

// Peek returns pending messages without consuming them.
func (s *Store) Peek(ctx context.Context, team, agent, user string) ([]model.Message, error) {
	rdb := s.readDB()
	if err := requireTeam(ctx, rdb, team); err != nil {
		return nil, err
	}
	inbox, err := resolveInbox(ctx, rdb, team, agent, user)
	if err != nil {
		return nil, err
	}
	rows, err := rdb.QueryContext(ctx,
		"SELECT id, body FROM messages WHERE team = ? AND inbox = ? ORDER BY id", team, inbox)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// MessageLog returns the newest limit sent messages, oldest first,
// optionally only those from one sender.
func (s *Store) MessageLog(ctx context.Context, team string, limit int, from string) ([]model.Message, error) {
	rdb := s.readDB()
	if err := requireTeam(ctx, rdb, team); err != nil {
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
	rows, err := rdb.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

type MessageLogEntry struct {
	ID      string         `json:"id"`
	Message *model.Message `json:"message,omitempty"`
	Event   map[string]any `json:"event,omitempty"`
}

type MessageLogPage struct {
	Items      []MessageLogEntry `json:"items"`
	NextCursor string            `json:"nextCursor"`
	HasMore    bool              `json:"hasMore"`
}

// MessageLogAfter reads the append-only message log without changing any inbox.
func (s *Store) MessageLogAfter(ctx context.Context, team, cursor string, limit int, includeChatEvents bool) (MessageLogPage, error) {
	page := MessageLogPage{Items: []MessageLogEntry{}, NextCursor: cursor}
	rdb := s.readDB()
	if err := requireTeam(ctx, rdb, team); err != nil {
		return page, err
	}
	if cursor == "latest" {
		var id int64
		err := rdb.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM events WHERE team=? AND (kind='message' OR (kind='chat-event' AND ?))", team, includeChatEvents).Scan(&id)
		page.NextCursor = strconv.FormatInt(id, 10)
		return page, err
	}
	after, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || after < 0 {
		return page, invalidf("Invalid message cursor")
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	rows, err := rdb.QueryContext(ctx, "SELECT id,kind,body FROM events WHERE team=? AND (kind='message' OR (kind='chat-event' AND ?)) AND id>? ORDER BY id LIMIT ?",
		team, includeChatEvents, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		var id int64
		var kind, body string
		var entry MessageLogEntry
		if err := rows.Scan(&id, &kind, &body); err != nil {
			return page, err
		}
		var target any = &entry.Message
		if kind == "chat-event" {
			target = &entry.Event
		}
		if err := json.Unmarshal([]byte(body), target); err != nil {
			return page, err
		}
		entry.ID = strconv.FormatInt(id, 10)
		page.NextCursor = entry.ID
		page.Items = append(page.Items, entry)
	}
	return page, rows.Err()
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
