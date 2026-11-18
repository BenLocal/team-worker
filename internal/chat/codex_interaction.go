package chat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const feishuCodexInteractionKey = "tw_codex_interaction"

var errCodexCardExpired = errors.New("The Codex card expired")

type codexInteraction struct {
	title, text string
	approve     bool
	deny        string
	permissions json.RawMessage
	questions   []model.ChatCodexQuestion
	wait        time.Duration
}

func parseCodexInteraction(msg codexMessage, thread, turn string, files map[string][]string) (codexInteraction, error) {
	var p struct {
		ThreadID, TurnID, ItemID, Command, Cwd, Reason, GrantRoot string
		NetworkApprovalContext                                    *struct{ Host, Protocol string }
		AvailableDecisions                                        []json.RawMessage
		Permissions                                               json.RawMessage
		Questions                                                 []model.ChatCodexQuestion
		AutoResolutionMs                                          int64
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID != thread || p.TurnID != turn {
		return codexInteraction{}, errCodexInteraction
	}
	i := codexInteraction{title: "Codex 请求审批", approve: true, deny: "decline", wait: 4 * time.Minute}
	var details []string
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		switch {
		case p.NetworkApprovalContext != nil:
			details = append(details, fmt.Sprintf("访问网络 %s（%s）", p.NetworkApprovalContext.Host, p.NetworkApprovalContext.Protocol))
		case p.Command != "":
			details = append(details, p.Command)
		default:
			return codexInteraction{}, errCodexInteraction
		}
		if len(p.AvailableDecisions) > 0 {
			has := func(decision string) bool {
				return slices.ContainsFunc(p.AvailableDecisions, func(raw json.RawMessage) bool {
					var tag string
					return json.Unmarshal(raw, &tag) == nil && tag == decision
				})
			}
			i.approve = has("accept")
			if !has("decline") {
				if !has("cancel") {
					return codexInteraction{}, errCodexInteraction
				}
				i.deny = "cancel"
			}
		}
	case "item/fileChange/requestApproval":
		if len(files[p.ItemID]) == 0 {
			return codexInteraction{}, errCodexInteraction
		}
		details = append(details, "修改以下文件", strings.Join(files[p.ItemID], "\n"))
	case "item/permissions/requestApproval":
		var permissions map[string]json.RawMessage
		if json.Unmarshal(p.Permissions, &permissions) != nil || len(permissions) == 0 {
			return codexInteraction{}, errCodexInteraction
		}
		i.permissions = p.Permissions
		details = append(details, "为本回合申请权限", string(p.Permissions))
	case "item/tool/requestUserInput":
		if len(p.Questions) == 0 || len(p.Questions) > 3 {
			return codexInteraction{}, errCodexInteraction
		}
		seen := make(map[string]bool, len(p.Questions))
		for _, q := range p.Questions {
			if q.ID == "" || seen[q.ID] || q.Secret || strings.TrimSpace(q.Question) == "" || len(q.Options) > 20 {
				return codexInteraction{}, errCodexInteraction
			}
			for _, option := range q.Options {
				if strings.TrimSpace(option.Label) == "" {
					return codexInteraction{}, errCodexInteraction
				}
			}
			seen[q.ID] = true
		}
		i.title, i.questions = "Codex 请求补充信息", p.Questions
		if p.AutoResolutionMs > 0 && p.AutoResolutionMs < i.wait.Milliseconds() {
			i.wait = time.Duration(p.AutoResolutionMs) * time.Millisecond
		}
		return i, nil
	default:
		return codexInteraction{}, errCodexInteraction
	}
	if p.Cwd != "" {
		details = append(details, "工作目录 "+p.Cwd)
	}
	if p.Reason != "" {
		details = append(details, p.Reason)
	}
	i.text = strings.Join(details, "\n")
	if len(i.text) > 10000 {
		return codexInteraction{}, errCodexInteraction
	}
	return i, nil
}

func (i codexInteraction) response(choice string, form map[string]any) (any, bool) {
	if len(i.questions) > 0 {
		if choice != "submit" {
			return nil, false
		}
		answers := make(map[string]any, len(i.questions))
		for n, q := range i.questions {
			field := codexFieldName(n)
			answer, _ := form[field].(string)
			if len(q.Options) > 0 {
				if q.Other && answer == "other" {
					answer, _ = form[field+"_other"].(string)
				} else if idx, err := strconv.Atoi(answer); err == nil && idx >= 0 && idx < len(q.Options) {
					answer = q.Options[idx].Label
				} else {
					return nil, false
				}
			}
			if strings.TrimSpace(answer) == "" || len(answer) > 4000 {
				return nil, false
			}
			answers[q.ID] = map[string][]string{"answers": {answer}}
		}
		return map[string]any{"answers": answers}, true
	}
	if choice != "deny" && (choice != "accept" || !i.approve) {
		return nil, false
	}
	if len(i.permissions) > 0 {
		permissions := json.RawMessage("{}")
		if choice == "accept" {
			permissions = i.permissions
		}
		return map[string]any{"permissions": permissions, "scope": "turn"}, true
	}
	decision := i.deny
	if choice == "accept" {
		decision = "accept"
	}
	return map[string]string{"decision": decision}, true
}

func codexFieldName(n int) string { return "answer_" + strconv.Itoa(n) }

type pendingCodexCard struct {
	origin      model.ChatOrigin
	message     string
	revision    int64
	interaction codexInteraction
	done        <-chan struct{}
	result      chan any
}

func (b *Bridge) codexCardAllowed(ctx context.Context, origin model.ChatOrigin, revision int64) bool {
	settings, err := b.Store.ChatSettings(ctx)
	return err == nil && settings.Revision == revision && settings.AllowsFeishuSender(b.Account, origin.Sender)
}

func (b *Bridge) waitForCodexCard(ctx context.Context, origin model.ChatOrigin, revision int64, interaction codexInteraction) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, interaction.wait)
	defer cancel()
	if !b.codexCardAllowed(ctx, origin, revision) {
		return nil, errCodexInteraction
	}
	token := rand.Text()
	out := origin.Reply(interaction.text)
	out.CardTitle = interaction.title
	out.CodexCard = &model.ChatCodexCard{Token: token, Approve: interaction.approve, Questions: interaction.questions}
	message, err := b.Deliver(ctx, out)
	if err != nil || message == "" {
		return nil, errCodexInteraction
	}
	pending := &pendingCodexCard{origin: origin, revision: revision, message: message, interaction: interaction,
		done: ctx.Done(), result: make(chan any, 1)}
	b.codexCards.Store(token, pending)
	defer b.codexCards.Delete(token)
	select {
	case <-ctx.Done():
		return nil, errCodexCardExpired
	case result := <-pending.result:
		if ctx.Err() != nil || !b.codexCardAllowed(ctx, origin, revision) {
			return nil, errCodexInteraction
		}
		return result, nil
	}
}

func (b *Bridge) receiveCodexCard(ctx context.Context, click *callback.CardActionTriggerRequest, token string) *callback.CardActionTriggerResponse {
	resp := &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "error", Content: "此交互已失效，或你无权提交。"}}
	entry, ok := b.codexCards.Load(token)
	if !ok {
		return resp
	}
	pending := entry.(*pendingCodexCard)
	if pending.origin.Sender != click.Operator.OpenID || pending.origin.Conversation != click.Context.OpenChatID ||
		pending.message != click.Context.OpenMessageID || !b.codexCardAllowed(ctx, pending.origin, pending.revision) {
		return resp
	}
	choice, _ := click.Action.Value["choice"].(string)
	result, valid := pending.interaction.response(choice, click.Action.FormValue)
	if !valid {
		resp.Toast.Content = "请检查选择并填完必填项。"
		return resp
	}
	select {
	case <-pending.done:
		return resp
	default:
	}
	if b.codexCards.CompareAndDelete(token, pending) {
		pending.result <- result
		resp.Toast.Type, resp.Toast.Content = "success", "已提交给 Codex。"
	}
	return resp
}
