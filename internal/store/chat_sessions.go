package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) ChatAssistantSession(ctx context.Context, key string) (string, error) {
	var threadID string
	err := s.db.QueryRowContext(ctx, "SELECT codex_thread_id FROM chat_assistant_sessions WHERE session_key=?", key).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return threadID, err
}

func (s *Store) SaveChatAssistantSession(ctx context.Context, key, threadID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_assistant_sessions(session_key,codex_thread_id) VALUES(?,?)
		ON CONFLICT(session_key) DO NOTHING`, key, threadID)
	return err
}
