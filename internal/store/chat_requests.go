package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

const chatRequestColumns = "id,account,message_id,sender,conversation,is_group,thread,body,state,team,binding_id,created_at,thread_id"

func scanChatRequest(row scanner) (model.ChatRequest, error) {
	var r model.ChatRequest
	err := row.Scan(&r.ID, &r.Account, &r.Origin.MessageID, &r.Origin.Sender, &r.Origin.Conversation,
		&r.Origin.Group, &r.Origin.Thread, &r.Body, &r.State, &r.Team, &r.BindingID, &r.CreatedAt, &r.Origin.ThreadID)
	return r, err
}

func getChatRequest(ctx context.Context, q queryer, id string) (model.ChatRequest, error) {
	r, err := scanChatRequest(q.QueryRowContext(ctx, "SELECT "+chatRequestColumns+" FROM chat_requests WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFoundf("Chat request not found")
	}
	return r, err
}

func (s *Store) ChatRequest(ctx context.Context, id string) (model.ChatRequest, error) {
	return getChatRequest(ctx, s.db, id)
}

func (s *Store) QueueChatRequest(ctx context.Context, r model.ChatRequest, app model.ChatAccount) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		o := r.Origin
		if r.State == "routing" && app.Provider == "feishu" {
			settings, err := readChatSettings(ctx, tx)
			if err != nil {
				return err
			}
			if settings.FeishuSenderGrant != "" {
				sender := settings.FeishuSender
				if sender == nil || sender.OpenID != o.Sender || sender.ChatAccount != app {
					r.State = "blocked"
				} else {
					r.BindingID = globalSenderPrefix + settings.FeishuSenderGrant + "-pending"
				}
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_requests (`+chatRequestColumns+`) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?
			WHERE NOT EXISTS (SELECT 1 FROM chat_jobs WHERE id=?)
			AND NOT EXISTS (SELECT 1 FROM chat_followups WHERE account=? AND message_id=?)
			ON CONFLICT DO NOTHING`, r.ID, r.Account, o.MessageID, o.Sender, o.Conversation, o.Group, o.Thread,
			r.Body, r.State, r.Team, r.BindingID, s.Now().Unix(), o.ThreadID, r.ID, r.Account, o.MessageID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 || r.State != "routing" {
			return err
		}
		return insertChatJob(ctx, tx, r.Account, ChatJob{
			ID: r.ID + "-route", Kind: "route", RequestID: r.ID, QueuedAt: s.Now().Unix(),
		})
	})
}

func (s *Store) RejectChatInbound(ctx context.Context, r model.ChatRequest, text string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var seen int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM chat_requests WHERE id=?
			UNION ALL SELECT 1 FROM chat_followups WHERE account=? AND message_id=? LIMIT 1`, r.ID, r.Account, r.Origin.MessageID).Scan(&seen)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return insertChatReply(ctx, tx, r, r.ID, r.Origin.Reply(text))
	})
}

func (s *Store) ReadyChatRequests(ctx context.Context, account string) ([]model.ChatRequest, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+chatRequestColumns+
		" FROM chat_requests WHERE account=? AND state='ready' ORDER BY created_at,id LIMIT 20", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []model.ChatRequest
	for rows.Next() {
		r, err := scanChatRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

func chatRequestAllowed(ctx context.Context, q queryer, r model.ChatRequest) (bool, error) {
	if r.State == "blocked" || r.State == "expired" {
		return false, nil
	}
	settings, err := readChatSettings(ctx, q)
	if err != nil {
		return false, err
	}
	account, allowed, err := chatSendAccount(ctx, q, settings, r.Team, r.BindingID, r.Origin.Sender)
	if err != nil || !allowed {
		return false, err
	}
	key, err := chatAccountKey(account)
	return key == r.Account, err
}

func (s *Store) ChatRequestAllowed(ctx context.Context, r model.ChatRequest) (bool, error) {
	return chatRequestAllowed(ctx, s.db, r)
}

func (s *Store) FinishChatRoute(ctx context.Context, r model.ChatRequest, binding *model.ChatBinding, response ChatJob) error {
	return s.finishChatRoute(ctx, r, binding, "clarifying", response, ChatJob{})
}

func (s *Store) FinishChatAssistant(ctx context.Context, r model.ChatRequest, action, response ChatJob) error {
	return s.finishChatRoute(ctx, r, nil, "answered", action, response)
}

func (s *Store) finishChatRoute(ctx context.Context, r model.ChatRequest, binding *model.ChatBinding, state string, response, additional ChatJob) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if binding != nil {
			r.Team, r.BindingID = binding.Team, binding.BindingID
			allowed, err := chatRequestAllowed(ctx, tx, r)
			if err != nil {
				return err
			}
			if !allowed {
				return conflictf("Team permissions changed during routing")
			}
			state = "ready"
		}
		res, err := tx.ExecContext(ctx, `UPDATE chat_requests SET state=?,team=?,binding_id=?
			WHERE id=? AND state='routing' AND created_at>?`, state, r.Team, r.BindingID, r.ID, s.Now().Add(-24*time.Hour).Unix())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if response.ID != "" {
			if err := insertChatJob(ctx, tx, r.Account, response); err != nil {
				return err
			}
		}
		if additional.ID != "" {
			if err := insertChatJob(ctx, tx, r.Account, additional); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE chat_jobs SET state='done',payload='' WHERE id=?", r.ID+"-route")
		return err
	})
}

func (s *Store) ResolveChatFollowup(ctx context.Context, incoming model.ChatRequest, parent string, binding model.ChatBinding) (bool, error) {
	account, origin := incoming.Account, incoming.Origin
	var handled bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		var duplicate int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM chat_followups WHERE account=? AND message_id=?
			UNION ALL SELECT 1 FROM chat_requests WHERE id=?
			UNION ALL SELECT 1 FROM chat_jobs WHERE id=? LIMIT 1`, account, origin.MessageID, incoming.ID, incoming.ID).Scan(&duplicate)
		if err == nil {
			handled = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.id FROM chat_requests r JOIN chat_jobs j ON j.id=r.id||'-clarify'
			WHERE r.account=? AND r.conversation=? AND r.sender=? AND r.is_group=? AND r.state='clarifying'
			AND r.created_at>? AND j.state='sent' AND (?='' OR j.result=?) LIMIT 2`, account, origin.Conversation,
			origin.Sender, origin.Group, s.Now().Add(-24*time.Hour).Unix(), parent, parent)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(ids) != 1 {
			return err
		}
		if err := checkChatSendPermission(ctx, tx, binding.Team, binding.BindingID, origin.Sender); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO chat_followups VALUES (?,?)", account, origin.MessageID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE chat_requests SET state='ready',team=?,binding_id=? WHERE id=?",
			binding.Team, binding.BindingID, ids[0])
		handled = err == nil
		return err
	})
	return handled, err
}

func (s *Store) DeliverChatRequest(ctx context.Context, id, provider string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		r, err := getChatRequest(ctx, tx, id)
		if err != nil || r.State != "ready" || r.CreatedAt <= s.Now().Add(-24*time.Hour).Unix() {
			return err
		}
		allowed, err := chatRequestAllowed(ctx, tx, r)
		if err != nil {
			return err
		}
		if !allowed {
			_, err := tx.ExecContext(ctx, "UPDATE chat_requests SET state='blocked' WHERE id=?", r.ID)
			return err
		}
		var inbox string
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN m.user_name='' THEN m.name ELSE m.user_name||'_'||m.name END
			FROM teams t JOIN members m ON m.team=t.name AND m.agent_id=t.lead_agent_id WHERE t.name=?`, r.Team).Scan(&inbox)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		source, approval := "direct", ""
		if r.Origin.Group {
			source, approval = "group", " --group-approved"
		}
		message := model.Message{
			Type: "message", From: provider + "-bridge", To: inbox, Timestamp: s.timestamp(), RequestID: r.ID,
			Content: fmt.Sprintf("Feishu request %s (%s). Submit a final result with team-worker chat reply %s %s%s <material>. Use --progress for an update, --image for a card preview, --image-reply for a separate image, --file for a downloadable attachment, or --doc 'TITLE=HTTPS_URL' for a cloud document.\n\n%s",
				r.ID, source, r.Team, r.ID, approval, r.Body),
		}
		if err := deliver(ctx, tx, r.Team, inbox, message); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE chat_requests SET state='delivered' WHERE id=?", id); err != nil {
			return err
		}
		text := fmt.Sprintf("已转给 %s 团队的 Leader。", r.Team)
		payload, err := json.Marshal(r.Origin.Reply(text))
		if err != nil {
			return err
		}
		return insertChatJob(ctx, tx, r.Account, ChatJob{ID: r.ID + "-receipt", Kind: "team-reply", RequestID: r.ID, Payload: payload, QueuedAt: s.Now().Unix()})
	})
}

func insertChatReply(ctx context.Context, tx *sql.Tx, r model.ChatRequest, jobID string, reply model.ChatOutbound) error {
	payload, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return insertChatJob(ctx, tx, r.Account, ChatJob{ID: jobID, Kind: "reply", Payload: payload})
}

func (s *Store) SubmitChatResult(ctx context.Context, team, id string, in model.ChatResultInput) error {
	in.Material = strings.TrimSpace(in.Material)
	if in.RunID == "" || in.AgentID == "" || in.Material == "" || len(in.Material) > 16000 {
		return invalidf("A current Leader run and up to 16000 bytes of material are required")
	}
	if err := validateChatAssets(in.Assets); err != nil {
		return err
	}
	kind := "progress"
	if in.Final {
		kind = "final"
	}
	jobID := chatResultJobID(id, kind, in.Material, in.Assets)
	return s.write(ctx, func(tx *sql.Tx) error {
		r, err := getChatRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Team != team {
			return notFoundf("Chat request not found in this team")
		}
		if r.Origin.Group && !in.GroupApproved {
			return invalidf("Confirm that the material may be shared in the originating group")
		}
		var valid int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM teams t JOIN agents a ON a.team=t.name AND a.agent_id=t.lead_agent_id
			WHERE t.name=? AND a.agent_id=? AND a.run_id=? AND a.status=? AND a.stop_requested=0`,
			team, in.AgentID, in.RunID, model.AgentRunning).Scan(&valid)
		if errors.Is(err, sql.ErrNoRows) {
			return conflictf("Only the current Leader run can submit chat results")
		}
		if err != nil {
			return err
		}
		allowed, err := chatRequestAllowed(ctx, tx, r)
		if err != nil {
			return err
		}
		if !allowed {
			return conflictf("The requester no longer has access to this team")
		}
		payload, err := json.Marshal(in.Material)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM chat_jobs WHERE id=?", jobID).Scan(&valid)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if r.State != "delivered" {
			return conflictf("The chat request is not open for results")
		}
		if in.Final {
			if _, err := tx.ExecContext(ctx, "UPDATE chat_requests SET state='finalized' WHERE id=?", id); err != nil {
				return err
			}
		}
		if err := insertChatJob(ctx, tx, r.Account, ChatJob{ID: jobID, Kind: kind, RequestID: id, Payload: payload, QueuedAt: s.Now().Unix()}); err != nil {
			return err
		}
		return insertChatAssets(ctx, tx, jobID, in.Assets)
	})
}

func (s *Store) FinishChatSummary(ctx context.Context, id string, reply model.ChatOutbound) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var embedded, separate bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM chat_result_assets WHERE job_id=? AND kind IN (?,?)),
			EXISTS(SELECT 1 FROM chat_result_assets WHERE job_id=? AND kind IN (?,?))`,
			id, model.ChatAssetPreview, model.ChatAssetLink, id, model.ChatAssetImage, model.ChatAssetFile).Scan(&embedded, &separate); err != nil {
			return err
		}
		reply.HasAssets = embedded
		payload, err := json.Marshal(reply)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE chat_jobs SET kind='team-reply',payload=?,attempts=0,ready_at=0
			WHERE id=? AND state='pending' AND kind IN ('progress','final')`, string(payload), id)
		if err != nil || !separate {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		attachment := model.ChatOutbound{ReplyTo: reply.ReplyTo, Conversation: reply.Conversation, Thread: reply.Thread, HasAssets: true}
		payload, err = json.Marshal(attachment)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_jobs(id,account,binding_id,kind,payload,request_id,queued_at)
			SELECT j.id||'-asset-'||a.position,j.account,j.binding_id,'team-reply',?,j.request_id,j.queued_at
			FROM chat_jobs j JOIN chat_result_assets a ON a.job_id=j.id
			WHERE j.id=? AND a.kind IN (?,?) ORDER BY a.position`, string(payload), id, model.ChatAssetImage, model.ChatAssetFile); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_result_assets SET job_id=job_id||'-asset-'||position
			WHERE job_id=? AND kind IN (?,?)`, id, model.ChatAssetImage, model.ChatAssetFile)
		return err
	})
}

func (s *Store) QueueChatTimers(ctx context.Context, account string) error {
	now := s.Now()
	const lateJobs = `j.account=? AND j.state='pending' AND j.kind IN ('route','progress','final') AND j.queued_at<=?
		AND NOT EXISTS(SELECT 1 FROM chat_jobs w WHERE w.id=j.id||'-wait')`
	const staleConfirmation = "account=? AND kind='assistant-confirm' AND state='pending' AND queued_at<=?"
	expiry, warningAt, confirmAt := now.Add(-24*time.Hour).Unix(), now.Add(-2*time.Minute).Unix(), now.Add(-chatConfirmWindow).Unix()
	var due bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_requests
		WHERE account=? AND state IN ('routing','clarifying','ready') AND created_at<=?)
		OR EXISTS(SELECT 1 FROM chat_jobs j WHERE `+lateJobs+`)
		OR EXISTS(SELECT 1 FROM chat_jobs WHERE `+staleConfirmation+`)`, account, expiry, account, warningAt, account, confirmAt).Scan(&due)
	if err != nil || !due {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE chat_jobs SET state='expired',payload='' WHERE state='pending' AND id IN "+
			"(SELECT request_id||'-confirm' FROM chat_jobs WHERE "+staleConfirmation+")", account, confirmAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE chat_jobs SET state='expired',payload='' WHERE "+staleConfirmation, account, confirmAt); err != nil {
			return err
		}
		expired, err := tx.QueryContext(ctx, "UPDATE chat_requests SET state='expired' WHERE account=? AND state IN ('routing','clarifying','ready') AND created_at<=? RETURNING "+chatRequestColumns,
			account, expiry)
		if err != nil {
			return err
		}
		var requests []model.ChatRequest
		for expired.Next() {
			r, err := scanChatRequest(expired)
			if err != nil {
				expired.Close()
				return err
			}
			requests = append(requests, r)
		}
		if err := errors.Join(expired.Err(), expired.Close()); err != nil {
			return err
		}
		for _, r := range requests {
			if err := insertChatReply(ctx, tx, r, r.ID+"-expired", r.Origin.Reply("请求已过期，尚未投递给团队。请重新发送。")); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE chat_jobs SET state='expired',payload='' WHERE id IN (?,?) AND state='pending'",
				r.ID+"-route", r.ID+"-clarify"); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT j.id,j.kind,r.message_id,r.conversation,r.thread
			FROM chat_jobs j JOIN chat_requests r ON r.id=j.request_id
			WHERE `+lateJobs+` LIMIT 20`, account, warningAt)
		if err != nil {
			return err
		}
		type warning struct {
			r        model.ChatRequest
			id, kind string
		}
		var warnings []warning
		for rows.Next() {
			w := warning{r: model.ChatRequest{Account: account}}
			if err := rows.Scan(&w.id, &w.kind, &w.r.Origin.MessageID, &w.r.Origin.Conversation, &w.r.Origin.Thread); err != nil {
				rows.Close()
				return err
			}
			warnings = append(warnings, w)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, w := range warnings {
			message := "正在等待路由 Agent 整理答复，恢复后会继续处理。"
			if w.kind == "route" {
				message = "正在等待路由 Agent 确定目标团队，恢复后会继续处理。"
			}
			if err := insertChatReply(ctx, tx, w.r, w.id+"-wait", w.r.Origin.Reply(message)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) BlockedChatRequests(ctx context.Context) ([]model.ChatBlockedItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,team,'Requester no longer has access',body,created_at,'request' FROM chat_requests WHERE state='blocked'
		UNION ALL SELECT r.id,r.team,j.result,j.payload,j.queued_at,j.kind FROM chat_jobs j
		JOIN chat_requests r ON r.id=j.request_id WHERE j.state='blocked' ORDER BY 5 DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.ChatBlockedItem{}
	for rows.Next() {
		var item model.ChatBlockedItem
		var timestamp int64
		var kind string
		if err := rows.Scan(&item.RequestID, &item.Team, &item.Reason, &item.Content, &timestamp, &kind); err != nil {
			return nil, err
		}
		switch kind {
		case "team-reply":
			var summary model.ChatOutbound
			if err := json.Unmarshal([]byte(item.Content), &summary); err != nil {
				return nil, err
			}
			item.Content = summary.Text
		case "progress", "final":
			if err := json.Unmarshal([]byte(item.Content), &item.Content); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) BlockChatRoute(ctx context.Context, r model.ChatRequest) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE chat_requests SET state='blocked' WHERE id=? AND state='routing'", r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE chat_jobs SET state='done' WHERE id=?", r.ID+"-route")
		return err
	})
}
