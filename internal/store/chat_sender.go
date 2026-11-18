package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/benlocal/team-worker/internal/model"
)

const globalSenderPrefix = "feishu-global-"

func globalChatBindingID(grant, team, createdAt string) string {
	id := sha256.Sum256([]byte(team + "\x00" + createdAt))
	return fmt.Sprintf("%s%s-%x", globalSenderPrefix, grant, id)
}

// ChatBindingsForSender resolves the account-wide Feishu grant or legacy team settings.
func (s *Store) ChatBindingsForSender(ctx context.Context, settings model.ChatSettings, account model.ChatAccount, sender string) (map[string]model.ChatBinding, error) {
	out := make(map[string]model.ChatBinding)
	if account.Provider != "feishu" || settings.FeishuSenderGrant == "" {
		for _, b := range settings.Bindings {
			if b.ChatAccount == account && b.Receive && slices.Contains(b.Senders, sender) {
				out[b.Alias] = b
			}
		}
		return out, nil
	}
	if settings.FeishuSender == nil || settings.FeishuSender.ChatAccount != account || settings.FeishuSender.OpenID != sender {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT name,created_at FROM teams")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var team, createdAt string
		if err := rows.Scan(&team, &createdAt); err != nil {
			return nil, err
		}
		out[team] = model.ChatBinding{ChatAccount: account, Team: team, Alias: team,
			BindingID: globalChatBindingID(settings.FeishuSenderGrant, team, createdAt)}
	}
	return out, rows.Err()
}

func chatSendAccount(ctx context.Context, q queryer, settings model.ChatSettings, team, bindingID, sender string) (model.ChatAccount, bool, error) {
	if settings.FeishuSenderGrant != "" && settings.FeishuSender != nil && settings.FeishuSender.OpenID == sender {
		var createdAt string
		err := q.QueryRowContext(ctx, "SELECT created_at FROM teams WHERE name=?", team).Scan(&createdAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return model.ChatAccount{}, false, err
		}
		if err == nil && bindingID == globalChatBindingID(settings.FeishuSenderGrant, team, createdAt) {
			return settings.FeishuSender.ChatAccount, true, nil
		}
	}
	for _, b := range settings.Bindings {
		if b.Provider == "feishu" && settings.FeishuSenderGrant != "" {
			continue
		}
		if b.Team == team && b.BindingID == bindingID && b.Receive && slices.Contains(b.Senders, sender) {
			return b.ChatAccount, true, nil
		}
	}
	return model.ChatAccount{}, false, nil
}
