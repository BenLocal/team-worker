package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/benlocal/team-worker/internal/model"
)

const chatSettingsKey = "chat_settings"
const chatConnectionPrefix = "chat-connection:"

func readChatSettings(ctx context.Context, q queryer) (model.ChatSettings, error) {
	settings := model.ChatSettings{Bindings: []model.ChatBinding{}}
	var data string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE name=?", chatSettingsKey).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	return settings, json.Unmarshal([]byte(data), &settings)
}

func (s *Store) ChatSettings(ctx context.Context) (model.ChatSettings, error) {
	return readChatSettings(ctx, s.db)
}

func checkChatSendPermission(ctx context.Context, tx *sql.Tx, team, bindingID, senderID string) error {
	settings, err := readChatSettings(ctx, tx)
	if err != nil {
		return err
	}
	_, allowed, err := chatSendAccount(ctx, tx, settings, team, bindingID, senderID)
	if err != nil || allowed {
		return err
	}
	return conflictf("Team chat binding or sender permission changed before delivery")
}

func putChatSetting(ctx context.Context, tx *sql.Tx, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(name,value) VALUES (?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", key, string(data))
	return err
}

func chatAccountKey(account model.ChatAccount) (string, error) {
	if err := validate(account.Provider, "chat provider"); err != nil {
		return "", err
	}
	if err := validateChatID(account.Tenant, "chat tenant ID"); err != nil {
		return "", err
	}
	if err := validateChatID(account.Application, "chat application ID"); err != nil {
		return "", err
	}
	key := strings.Join([]string{account.Provider, account.Tenant, account.Application}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key))), nil
}

func validateChatID(value, kind string) error {
	if value == "" || len(value) > 512 || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return invalidf("Invalid %s", kind)
	}
	return nil
}

func (s *Store) SetChatSettings(ctx context.Context, settings model.ChatSettings) (model.ChatSettings, error) {
	err := s.write(ctx, func(tx *sql.Tx) error {
		current, err := readChatSettings(ctx, tx)
		if err != nil {
			return err
		}
		if current.Revision != settings.Revision {
			return conflictf("Chat settings changed. Reload before saving")
		}
		if !settings.UpdateRecipient && settings.Recipient == nil {
			settings.Recipient = current.Recipient
		}
		settings.UpdateRecipient = false
		settings.FeishuSenderGrant = current.FeishuSenderGrant
		if !settings.UpdateFeishuSender {
			settings.FeishuSender = current.FeishuSender
		} else {
			if sender := settings.FeishuSender; sender != nil {
				if _, err := chatAccountKey(sender.ChatAccount); err != nil {
					return err
				}
				if sender.Provider != "feishu" || !strings.HasPrefix(sender.OpenID, "ou_") || len(sender.OpenID) <= 3 {
					return invalidf("The authorized sender must be a Feishu user open_id")
				}
				if err := validateChatID(sender.OpenID, "sender open_id"); err != nil {
					return err
				}
			}
			if settings.FeishuSenderGrant == "" || !sameRecipient(settings.FeishuSender, current.FeishuSender) {
				settings.FeishuSenderGrant = randomHex(24)
			}
		}
		settings.UpdateFeishuSender = false
		if settings.Recipient != nil {
			if _, err := chatAccountKey(settings.Recipient.ChatAccount); err != nil {
				return err
			}
			if settings.Recipient.Provider != "feishu" || !strings.HasPrefix(settings.Recipient.OpenID, "ou_") {
				return invalidf("The default recipient must be a Feishu user open_id")
			}
			if err := validateChatID(settings.Recipient.OpenID, "recipient open_id"); err != nil {
				return err
			}
		}
		recipientChanged := !sameRecipient(settings.Recipient, current.Recipient)
		rows, err := tx.QueryContext(ctx, `SELECT t.name,t.created_at,COALESCE(MAX(e.id),0)
			FROM teams t LEFT JOIN events e ON e.team=t.name AND e.kind IN ('message','chat-event')
			GROUP BY t.name`)
		if err != nil {
			return err
		}
		type teamState struct {
			createdAt string
			cursor    int64
		}
		teamsByName := make(map[string]teamState)
		for rows.Next() {
			var team string
			var state teamState
			if err := rows.Scan(&team, &state.createdAt, &state.cursor); err != nil {
				rows.Close()
				return err
			}
			teamsByName[team] = state
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		conversations, aliases, teams := map[string]bool{}, map[string]bool{}, map[string]bool{}
		previous := make(map[string]model.ChatBinding, len(current.Bindings))
		for _, binding := range current.Bindings {
			previous[binding.Team] = binding
		}
		for i := range settings.Bindings {
			binding := &settings.Bindings[i]
			account, err := chatAccountKey(binding.ChatAccount)
			if err != nil {
				return err
			}
			if binding.Provider != "feishu" || binding.Conversation != "" {
				if err := validateChatID(binding.Conversation, "conversation ID"); err != nil {
					return err
				}
			}
			if err := validate(binding.Alias, "team alias"); err != nil {
				return err
			}
			for _, sender := range binding.Senders {
				if err := validateChatID(sender, "sender ID"); err != nil {
					return err
				}
			}
			for _, event := range binding.Events {
				if !slices.Contains([]string{"TaskCompleted", "WorkerCrash", "BudgetExceeded"}, event) {
					return invalidf("Unsupported chat event %q", event)
				}
			}
			if (binding.Provider != "feishu" && conversations[account+":"+binding.Conversation]) || aliases[account+":"+binding.Alias] || teams[binding.Team] {
				return invalidf("Chat teams, aliases and conversations must be unique")
			}
			conversations[account+":"+binding.Conversation], aliases[account+":"+binding.Alias], teams[binding.Team] = true, true, true
			state, ok := teamsByName[binding.Team]
			if !ok {
				return notFoundf("Team %q not found", binding.Team)
			}
			if binding.TeamCreatedAt != "" && binding.TeamCreatedAt != state.createdAt {
				return conflictf("Team %q was recreated. Remove and add its binding", binding.Team)
			}
			binding.TeamCreatedAt = state.createdAt
			old := previous[binding.Team]
			binding.BindingID, binding.StartCursor = old.BindingID, old.StartCursor
			if old.BindingID == "" || old.TeamCreatedAt != state.createdAt || old.ChatAccount != binding.ChatAccount || old.Conversation != binding.Conversation {
				binding.BindingID = randomHex(24)
				binding.StartCursor = strconv.FormatInt(state.cursor, 10)
			}
			binding.NoticeID, binding.NoticeStart = "", ""
			if recipient := settings.Recipient; recipient != nil && recipient.ChatAccount == binding.ChatAccount {
				binding.NoticeID, binding.NoticeStart = old.NoticeID, old.NoticeStart
				if recipientChanged || old.BindingID != binding.BindingID || binding.NoticeID == "" {
					binding.NoticeID, binding.NoticeStart = randomHex(24), strconv.FormatInt(state.cursor, 10)
				}
			}
		}
		if settings.Bindings == nil {
			settings.Bindings = []model.ChatBinding{}
		}
		settings.Revision++
		return saveChatSettings(ctx, tx, settings)
	})
	return settings, err
}

func sameRecipient(a, b *model.ChatRecipient) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func saveChatSettings(ctx context.Context, tx *sql.Tx, settings model.ChatSettings) error {
	if err := putChatSetting(ctx, tx, chatSettingsKey, settings); err != nil {
		return err
	}
	const bindings = "json_each((SELECT value FROM settings WHERE name=?),'$.bindings')"
	if _, err := tx.ExecContext(ctx, `UPDATE chat_requests AS r SET state='blocked'
		WHERE (r.state IN ('ready','delivered') OR (r.state IN ('routing','clarifying') AND r.binding_id LIKE ?) OR (r.state='finalized' AND EXISTS(
			SELECT 1 FROM chat_jobs j WHERE j.request_id=r.id AND j.state='pending')))
		AND NOT (r.binding_id LIKE ? AND ?)
		AND NOT EXISTS (SELECT 1 FROM `+bindings+` b, json_each(b.value,'$.senders') u
			WHERE json_extract(b.value,'$.bindingId')=r.binding_id AND json_extract(b.value,'$.receive')=1 AND u.value=r.sender
			AND (?='' OR json_extract(b.value,'$.provider')<>'feishu'))`,
		globalSenderPrefix+"%", globalSenderPrefix+settings.FeishuSenderGrant+"-%", settings.FeishuSender != nil, chatSettingsKey, settings.FeishuSenderGrant); err != nil {
		return err
	}
	if err := blockUnauthorizedChatJobs(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE chat_jobs SET state='done',payload='',result='Recipient changed'
		WHERE kind='notice' AND state='pending' AND NOT EXISTS (SELECT 1 FROM `+bindings+` b
			WHERE json_extract(b.value,'$.noticeId')=chat_jobs.binding_id)`, chatSettingsKey)
	return err
}

func blockUnauthorizedChatJobs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE chat_jobs SET state='blocked',result='Requester no longer has access'
		WHERE state='pending' AND request_id IN (SELECT id FROM chat_requests WHERE state='blocked')
		AND kind IN ('route','progress','final','team-reply')`)
	return err
}

// resetTeamChatBinding invalidates queued deliveries when team history is
// replaced. A different team incarnation must be bound explicitly.
func resetTeamChatBinding(ctx context.Context, tx *sql.Tx, team string, restore bool) error {
	settings, err := readChatSettings(ctx, tx)
	if err != nil {
		return err
	}
	if settings.FeishuSenderGrant != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE chat_requests SET state='blocked'
			WHERE team=? AND binding_id LIKE ? AND state IN ('ready','delivered','finalized')`,
			team, globalSenderPrefix+"%"); err != nil {
			return err
		}
		if err := blockUnauthorizedChatJobs(ctx, tx); err != nil {
			return err
		}
	}
	for i, binding := range settings.Bindings {
		if binding.Team != team {
			continue
		}
		var createdAt string
		var cursor int64
		if restore {
			err := tx.QueryRowContext(ctx, `SELECT t.created_at,COALESCE(MAX(e.id),0)
				FROM teams t LEFT JOIN events e ON e.team=t.name AND e.kind IN ('message','chat-event')
				WHERE t.name=? GROUP BY t.name`, team).Scan(&createdAt, &cursor)
			if err != nil {
				return err
			}
		}
		if restore && binding.TeamCreatedAt == createdAt {
			binding.BindingID = randomHex(24)
			binding.StartCursor = strconv.FormatInt(cursor, 10)
			if binding.NoticeID != "" {
				binding.NoticeID, binding.NoticeStart = randomHex(24), binding.StartCursor
			}
			settings.Bindings[i] = binding
		} else {
			settings.Bindings = slices.Delete(settings.Bindings, i, i+1)
		}
		settings.Revision++
		return saveChatSettings(ctx, tx, settings)
	}
	return nil
}

func (s *Store) ReportChatConnection(ctx context.Context, connection model.ChatConnection) error {
	key, err := chatAccountKey(connection.ChatAccount)
	if err != nil {
		return err
	}
	connection.SeenAt = s.timestamp()
	return s.write(ctx, func(tx *sql.Tx) error {
		return putChatSetting(ctx, tx, chatConnectionPrefix+key, connection)
	})
}

func (s *Store) ChatConnections(ctx context.Context) ([]model.ChatConnection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT value FROM settings WHERE name LIKE ? ORDER BY name", chatConnectionPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := []model.ChatConnection{}
	for rows.Next() {
		var connection model.ChatConnection
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &connection); err != nil {
			return nil, err
		}
		seen, err := time.Parse(time.RFC3339Nano, connection.SeenAt)
		if err != nil {
			return nil, err
		}
		connection.Connected = connection.Connected && s.Now().Sub(seen) < time.Minute
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}
