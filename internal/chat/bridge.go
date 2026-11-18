// Package chat routes chat messages through the team-worker store.
package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type Inbound struct {
	ID, Sender, Conversation, Text, Parent string
	Group, Mentioned, Thread               bool
}

type Outbound = model.ChatOutbound

type DeliveryError struct {
	Retryable bool
	Message   string
}

func (e *DeliveryError) Error() string { return e.Message }

type Bridge struct {
	Store   *store.Store
	Account model.ChatAccount
	Board   string
	Log     *slog.Logger
	Deliver func(context.Context, Outbound) (string, error)
	Upload  func(context.Context, model.ChatAsset) (string, error)
	Agent   ChatAgent

	lastReported     time.Time
	reportedRevision int64
	reportedOnline   bool
}

type command struct {
	Inbound
	Alias, Body string
	Prepared    *store.SendInput
}

type notice struct {
	Kind, Text, Preview string
}

func digest(parts ...string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
}

func (b *Bridge) accountKey() string {
	return digest(b.Account.Provider, b.Account.Tenant, b.Account.Application)
}

func word(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimLeftFunc(s[idx:], unicode.IsSpace)
}

func (b *Bridge) binding(settings model.ChatSettings, msg command) *model.ChatBinding {
	for _, binding := range settings.Bindings {
		if binding.ChatAccount != b.Account || !binding.Receive || !slices.Contains(binding.Senders, msg.Sender) {
			continue
		}
		if (msg.Group && binding.Conversation == msg.Conversation) || (!msg.Group && binding.Alias == msg.Alias) {
			return &binding
		}
	}
	return nil
}

func jobJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err) // Only fixed structs with string and boolean fields are used.
	}
	return data
}

// Accept acknowledges an inbound message after recording it or resolving a followup.
func (b *Bridge) Accept(ctx context.Context, msg Inbound) error {
	text := strings.TrimSpace(msg.Text)
	if text == "" || msg.ID == "" || msg.Sender == "" || msg.Conversation == "" {
		return nil
	}
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	r := model.ChatRequest{ID: digest(b.accountKey(), msg.ID), Account: b.accountKey(), Body: text, State: "routing",
		Origin: model.ChatOrigin{MessageID: msg.ID, Sender: msg.Sender, Conversation: msg.Conversation, Group: msg.Group, Thread: msg.Thread}}
	bindings, err := b.Store.ChatBindingsForSender(ctx, settings, b.Account, msg.Sender)
	if err != nil {
		return err
	}
	if binding, ok := bindings[text]; ok && (!msg.Group || msg.Parent != "") {
		handled, err := b.Store.ResolveChatFollowup(ctx, r, msg.Parent, binding)
		if err != nil || handled {
			return err
		}
	}
	if msg.Group && !msg.Mentioned {
		return nil
	}
	if len(text) > 16000 {
		return b.Store.RejectChatInbound(ctx, r, "消息过长，请缩短后重新发送。")
	}
	prefix, rest := word(text)
	if prefix == "/tw" {
		op, body := word(rest)
		if op != "send" || body == "" {
			return b.Store.RejectChatInbound(ctx, r, "用法：/tw send <团队> <正文>。也可以直接描述你要发给团队的请求。")
		}
		alias, content := word(body)
		if binding, ok := bindings[alias]; ok {
			if content == "" {
				return b.Store.RejectChatInbound(ctx, r, "请在团队名后填写消息正文。")
			}
			r.State, r.Team, r.BindingID, r.Body = "ready", binding.Team, binding.BindingID, content
		} else if settings.FeishuSenderGrant == "" && slices.ContainsFunc(settings.Bindings, func(binding model.ChatBinding) bool {
			return binding.ChatAccount == b.Account && binding.Alias == alias
		}) {
			return b.Store.RejectChatInbound(ctx, r, "没有权限或该团队暂未启用飞书收件。")
		}
		if r.State == "routing" {
			teams, err := b.Store.ListTeams(ctx)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(teams, func(t model.TeamSummary) bool { return t.Name == alias }) {
				return b.Store.RejectChatInbound(ctx, r, "没有权限或该团队暂未启用飞书收件。")
			}
			r.Body = body
		}
	}
	if len(bindings) == 0 {
		return b.Store.RejectChatInbound(ctx, r, "没有权限或该团队暂未启用飞书收件。")
	}
	return b.Store.QueueChatRequest(ctx, r, b.Account)
}

func reply(msg Inbound, text string) Outbound {
	return Outbound{Conversation: msg.Conversation, ReplyTo: msg.ID, Thread: msg.Thread, Text: text}
}

func (b *Bridge) processCommand(ctx context.Context, settings model.ChatSettings, job store.ChatJob) error {
	var cmd command
	if err := json.Unmarshal(job.Payload, &cmd); err != nil {
		return err
	}
	binding := b.binding(settings, cmd)
	message := "发送失败：绑定或发送权限已变更。"
	if binding != nil && binding.BindingID == job.BindingID {
		leader, err := b.Store.Leader(ctx, binding.Team)
		switch {
		case err != nil && store.KindOf(err) == 0:
			return err
		case err != nil:
			message = "发送失败：找不到团队的 Leader。"
		default:
			if cmd.Prepared == nil {
				cmd.Prepared = &store.SendInput{
					From: b.Account.Provider + "-bridge", To: leader.InboxName(), ExpectedLeader: leader.AgentID,
					ChatBindingID: binding.BindingID, ChatSenderID: cmd.Sender, IdempotencyKey: job.ID,
					Content: fmt.Sprintf("Chat sender (%s): %s\n\n%s", b.Account.Provider, cmd.Sender, cmd.Body),
				}
				job.Payload = jobJSON(cmd)
				if err := b.Store.SaveChatJobPayload(ctx, job); err != nil {
					return err
				}
			}
			_, err := b.Store.Send(ctx, binding.Team, *cmd.Prepared)
			if err == nil {
				message = fmt.Sprintf("已转给 %s 团队的 Leader。", binding.Team)
			} else if store.KindOf(err) == 0 {
				return err
			} else {
				message = "发送失败：绑定、权限或目标 Leader 已变更，请重新发送。"
			}
		}
	}
	job.Kind, job.Payload = "reply", jobJSON(reply(cmd.Inbound, message))
	return b.Store.ReplaceChatJob(ctx, job)
}

func formatNotice(binding model.ChatBinding, entry store.MessageLogEntry) notice {
	n := notice{}
	if message := entry.Message; message != nil && message.To == "operator" {
		n.Kind = string(message.Type)
		switch n.Kind {
		case "message":
			n.Text = fmt.Sprintf("[%s] %s 发来一条汇报\n%s", binding.Team, message.From, message.Timestamp)
			if binding.Preview {
				content := []rune(message.Content)
				n.Preview = string(content[:min(len(content), 200)])
				if len(content) > 200 {
					n.Preview += "…"
				}
			}
		case "decision_request":
			n.Text = fmt.Sprintf("[%s] %s 发起请示 %s\n%s", binding.Team, message.From, message.RequestID, message.Timestamp)
		}
		return n
	}
	n.Kind, _ = entry.Event["type"].(string)
	taskID, _ := entry.Event["task_id"].(string)
	member, _ := entry.Event["agent_name"].(string)
	stamp, _ := entry.Event["timestamp"].(string)
	switch n.Kind {
	case "TaskCompleted":
		n.Text = fmt.Sprintf("[%s] 任务 %s 已完成\n%s", binding.Team, taskID, stamp)
	case "WorkerCrash":
		n.Text = fmt.Sprintf("[%s] 成员 %s 异常退出\n%s", binding.Team, member, stamp)
	case "BudgetExceeded":
		n.Text = fmt.Sprintf("[%s] 团队预算已达到上限\n%s", binding.Team, stamp)
	}
	return n
}

func noticeEnabled(binding model.ChatBinding, kind string) bool {
	switch kind {
	case "message":
		return binding.Reports
	case "decision_request":
		return binding.Requests
	default:
		return slices.Contains(binding.Events, kind)
	}
}

func (b *Bridge) collect(ctx context.Context, binding model.ChatBinding) error {
	start := binding.NoticeStart
	if start == "" {
		start = "latest"
	}
	cursor, err := b.Store.ChatCursor(ctx, binding.NoticeID, start)
	if err != nil {
		return err
	}
	page, err := b.Store.MessageLogAfter(ctx, binding.Team, cursor, 100, true)
	if err != nil {
		return err
	}
	if cursor == page.NextCursor {
		return nil
	}
	var jobs []store.ChatJob
	for _, entry := range page.Items {
		n := formatNotice(binding, entry)
		if n.Text != "" && noticeEnabled(binding, n.Kind) {
			jobs = append(jobs, store.ChatJob{ID: digest(binding.NoticeID, entry.ID), Kind: "notice", Payload: jobJSON(n)})
		}
	}
	return b.Store.EnqueueChatPage(ctx, b.accountKey(), binding.NoticeID, page.NextCursor, jobs)
}

func (b *Bridge) send(ctx context.Context, job store.ChatJob, message Outbound) error {
	claimed, err := b.Store.ClaimChatDelivery(ctx, job.ID)
	if err != nil || !claimed {
		return err
	}
	id, err := b.Deliver(ctx, message)
	if err == nil {
		return b.Store.SetChatJobState(ctx, job.ID, "sent", id)
	}
	return b.deliveryFailure(ctx, job, err)
}

func (b *Bridge) deliveryFailure(ctx context.Context, job store.ChatJob, err error) error {
	var delivery *DeliveryError
	if errors.As(err, &delivery) {
		if delivery.Retryable {
			return b.Store.RetryChatJob(ctx, job)
		}
		return b.Store.SetChatJobState(ctx, job.ID, "failed", delivery.Message)
	}
	return b.Store.SetChatJobState(ctx, job.ID, "unknown", "Delivery result is unknown")
}

func (b *Bridge) deliverNotices(ctx context.Context, bindingID string) error {
	jobs, err := b.Store.PendingChatJobs(ctx, b.accountKey(), "notice", bindingID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		settings, err := b.Store.ChatSettings(ctx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(settings.Bindings, func(item model.ChatBinding) bool {
			return item.ChatAccount == b.Account && item.NoticeID == bindingID
		})
		if i < 0 || !settings.Bindings[i].Notify || settings.Recipient == nil || settings.Recipient.ChatAccount != b.Account {
			return nil
		}
		current := settings.Bindings[i]
		var n notice
		if err := json.Unmarshal(job.Payload, &n); err != nil {
			return err
		}
		if !noticeEnabled(current, n.Kind) {
			if err := b.Store.SetChatJobState(ctx, job.ID, "failed", "Notification disabled"); err != nil {
				return err
			}
			continue
		}
		if current.Preview && n.Preview != "" {
			n.Text += "\n" + n.Preview
		}
		if b.Board != "" {
			n.Text += "\n" + b.Board
		}
		if err := b.send(ctx, job, Outbound{OpenID: settings.Recipient.OpenID, Text: n.Text}); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) sync(ctx context.Context, online bool) error {
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	if time.Since(b.lastReported) >= 20*time.Second || b.reportedOnline != online || b.reportedRevision != settings.Revision {
		if err := b.Store.ReportChatConnection(ctx, model.ChatConnection{ChatAccount: b.Account, Connected: online, AppliedRevision: settings.Revision}); err != nil {
			return err
		}
		b.lastReported, b.reportedOnline, b.reportedRevision = time.Now(), online, settings.Revision
	}
	if err := b.Store.QueueChatTimers(ctx, b.accountKey()); err != nil {
		return err
	}
	ready, err := b.Store.ReadyChatRequests(ctx, b.accountKey())
	if err != nil {
		return err
	}
	for _, r := range ready {
		if err := b.Store.DeliverChatRequest(ctx, r.ID, b.Account.Provider); err != nil {
			return err
		}
	}
	commands, err := b.Store.PendingChatJobs(ctx, b.accountKey(), "command", "")
	if err != nil {
		return err
	}
	for _, job := range commands {
		if err := b.processCommand(ctx, settings, job); err != nil {
			return err
		}
	}
	if online {
		replies, err := b.Store.PendingChatJobs(ctx, b.accountKey(), "reply", "")
		if err != nil {
			return err
		}
		for _, job := range replies {
			var out Outbound
			if err := json.Unmarshal(job.Payload, &out); err != nil {
				return err
			}
			if err := b.send(ctx, job, out); err != nil {
				return err
			}
		}
		if err := b.sendTeamReplies(ctx); err != nil {
			return err
		}
	}
	for _, binding := range settings.Bindings {
		if binding.ChatAccount != b.Account || !binding.Notify || binding.NoticeID == "" {
			continue
		}
		if err := b.collect(ctx, binding); err != nil {
			if store.KindOf(err) == store.KindNotFound {
				continue
			}
			return err
		}
		if online {
			if err := b.deliverNotices(ctx, binding.NoticeID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bridge) Run(ctx context.Context, online func() bool) {
	if err := b.Store.RecoverChatJobs(ctx, b.accountKey()); err != nil {
		b.Log.Warn("recover chat deliveries", "err", err)
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	registered := false
	for {
		up := online()
		registered = registered || up
		if registered {
			if err := b.sync(ctx, up); err != nil && ctx.Err() == nil {
				b.Log.Warn("sync chat integration", "provider", b.Account.Provider, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
