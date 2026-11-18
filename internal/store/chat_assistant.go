package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

const chatConfirmWindow = 10 * time.Minute

func ChatAssistantWriteAllowed(settings model.ChatSettings, account model.ChatAccount, request model.ChatRequest) bool {
	return settings.AllowsFeishuSender(account, request.Origin.Sender) &&
		request.BindingID == globalSenderPrefix+settings.FeishuSenderGrant+"-pending"
}

func (s *Store) ConfirmChatAction(ctx context.Context, app model.ChatAccount, incoming model.ChatRequest, token string) (bool, error) {
	var accepted bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		origin := incoming.Origin
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_followups WHERE account=? AND message_id=?)`,
			incoming.Account, origin.MessageID).Scan(&accepted); err != nil || accepted {
			return err
		}
		settings, err := readChatSettings(ctx, tx)
		if err != nil || !settings.AllowsFeishuSender(app, origin.Sender) {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE chat_jobs SET kind='assistant-write'
			WHERE id=? AND account=? AND kind='assistant-confirm' AND state='pending' AND queued_at>?
			AND EXISTS (SELECT 1 FROM chat_requests r JOIN chat_jobs prompt ON prompt.id=r.id||'-confirm'
				WHERE r.id=chat_jobs.request_id AND r.sender=? AND r.conversation=? AND r.is_group=?
				AND r.binding_id=? AND prompt.state='sent')`,
			token, incoming.Account, s.Now().Add(-chatConfirmWindow).Unix(), origin.Sender, origin.Conversation, origin.Group,
			globalSenderPrefix+settings.FeishuSenderGrant+"-pending")
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO chat_followups VALUES (?,?)", incoming.Account, origin.MessageID)
		accepted = err == nil
		return err
	})
	return accepted, err
}

func (s *Store) CompleteChatAction(ctx context.Context, id string, reply model.ChatOutbound) error {
	payload, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE chat_jobs SET kind='reply',state='pending',payload=?,attempts=0,ready_at=0
		WHERE id=? AND kind='assistant-write' AND state='sending'`, string(payload), id)
	return err
}

func (s *Store) ClaimChatAction(ctx context.Context, id string, revision int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_jobs SET state='sending',result=''
		WHERE id=? AND kind='assistant-write' AND state='pending'
		AND (SELECT json_extract(value,'$.revision') FROM settings WHERE name=?)=?`, id, chatSettingsKey, revision)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
