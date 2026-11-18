package chat

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type agentInput struct {
	Request    string   `json:"request"`
	Teams      []string `json:"teams,omitempty"`
	Result     string   `json:"result,omitempty"`
	Context    string   `json:"context,omitempty"`
	SessionKey string   `json:"-"`
	Scoped     bool     `json:"-"`
	interact   func(context.Context, codexInteraction) (any, error)
}

type agentOutput struct {
	assistantAction
	Summary  string `json:"summary"`
	threadID string
}

type ChatAgent func(context.Context, string, agentInput) (agentOutput, error)

var teamAssistantActions = []string{"answer", "route", "list_teams", "team_status", "list_members", "list_tasks", "create_team", "create_task"}

func assistantSessionKey(r model.ChatRequest, revision int64) string {
	topic := r.Origin.ThreadID
	if r.Origin.Thread && topic == "" {
		topic = r.Origin.MessageID
	}
	return digest(r.Account, r.Origin.Sender, r.Origin.Conversation, topic, strconv.FormatInt(revision, 10))
}

func (b *Bridge) runAgent(ctx context.Context) {
	if b.Agent == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := b.processAgentJobs(ctx); err != nil && ctx.Err() == nil {
			b.Log.Warn("process chat routing jobs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bridge) processAgentJobs(ctx context.Context) error {
	for _, kind := range []string{"route", "progress", "final"} {
		jobs, err := b.Store.PendingChatJobs(ctx, b.accountKey(), kind, "")
		if err != nil {
			return err
		}
		for _, job := range jobs {
			r, err := b.Store.ChatRequest(ctx, job.RequestID)
			if err != nil {
				return err
			}
			switch kind {
			case "route":
				err = b.routeRequest(ctx, r, job)
			default:
				err = b.summarizeResult(ctx, r, job)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bridge) routeRequest(ctx context.Context, r model.ChatRequest, job store.ChatJob) error {
	if r.State != "routing" {
		return nil
	}
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	candidates, err := b.Store.ChatBindingsForSender(ctx, settings, b.Account, r.Origin.Sender)
	if err != nil {
		return err
	}
	if len(candidates) == 0 && !settings.AllowsFeishuSender(b.Account, r.Origin.Sender) {
		return b.Store.BlockChatRoute(ctx, r)
	}
	aliases := slices.Sorted(maps.Keys(candidates))
	out, err := b.Agent(ctx, "route", agentInput{Request: r.Body, Teams: aliases, SessionKey: assistantSessionKey(r, settings.Revision),
		Scoped: !settings.AllowsFeishuSender(b.Account, r.Origin.Sender)})
	// Keep the turn in the conversation, but use the board for an explicit status query.
	if alias := directTeamStatusAlias(r.Body, candidates); alias != "" {
		return b.queueAssistantAction(ctx, r, settings, candidates, assistantAction{Action: "team_status", Alias: alias})
	}
	if err != nil {
		if errors.Is(err, errCodexInteraction) || errors.Is(err, errCodexToolInInternalTurn) {
			return b.finishAssistantAnswer(ctx, r, settings.Revision, assistantFailureText(err))
		}
		return b.Store.RetryChatJob(ctx, job)
	}
	switch out.Action {
	case "list_teams", "team_status", "list_members", "list_tasks", "create_team", "create_task":
		if out.Runtime != "" && out.Action == "create_team" {
			return b.answerAssistantQuestion(ctx, r, job, settings, candidates, "")
		}
		return b.queueAssistantAction(ctx, r, settings, candidates, out.assistantAction)
	}
	if binding, ok := candidates[out.Alias]; ok && (out.Action == "" || out.Action == "route") {
		err := b.Store.FinishChatRoute(ctx, r, &binding, store.ChatJob{})
		if store.KindOf(err) == store.KindConflict {
			return b.Store.RetryChatJob(ctx, job)
		}
		return err
	}
	if out.Action != "route" {
		return b.answerAssistantQuestion(ctx, r, job, settings, candidates, out.Alias)
	}
	settings, err = b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	remaining, err := b.Store.ChatBindingsForSender(ctx, settings, b.Account, r.Origin.Sender)
	if err != nil {
		return err
	}
	if len(remaining) == 0 && !settings.AllowsFeishuSender(b.Account, r.Origin.Sender) {
		return b.Store.BlockChatRoute(ctx, r)
	}
	text := "这条消息要发给哪个团队？请回复团队别名。"
	return b.Store.FinishChatRoute(ctx, r, nil, store.ChatJob{
		ID: r.ID + "-clarify", Kind: "reply", Payload: jobJSON(r.Origin.Reply(text)),
	})
}

func (b *Bridge) summarizeResult(ctx context.Context, r model.ChatRequest, job store.ChatJob) error {
	allowed, err := b.Store.ChatRequestAllowed(ctx, r)
	if err != nil {
		return err
	}
	if !allowed {
		return b.Store.SetChatJobState(ctx, job.ID, "blocked", "Requester no longer has access")
	}
	var material string
	if err := json.Unmarshal(job.Payload, &material); err != nil {
		return err
	}
	out, err := b.Agent(ctx, "summary", agentInput{Request: r.Body, Result: material, Scoped: true})
	summary := strings.TrimSpace(out.Summary)
	if errors.Is(err, errCodexInteraction) || errors.Is(err, errCodexToolInInternalTurn) {
		summary = assistantFailureText(err)
	} else if err != nil || summary == "" || len(summary) > 16000 {
		return b.Store.RetryChatJob(ctx, job)
	}
	label := "进展"
	if job.Kind == "final" {
		label = "最终结果"
	}
	title := fmt.Sprintf("[%s] %s", r.Team, label)
	reply := r.Origin.Reply(summary)
	if b.Account.Provider == "feishu" {
		reply.CardTitle = title
	} else {
		reply.Text = title + "\n" + summary
	}
	return b.Store.FinishChatSummary(ctx, job.ID, reply)
}

func (b *Bridge) sendTeamReplies(ctx context.Context) error {
	jobs, err := b.Store.PendingChatJobs(ctx, b.accountKey(), "team-reply", "")
	if err != nil {
		return err
	}
	for _, job := range jobs {
		r, err := b.Store.ChatRequest(ctx, job.RequestID)
		if err != nil {
			return err
		}
		allowed, err := b.Store.ChatRequestAllowed(ctx, r)
		if err != nil {
			return err
		}
		if !allowed {
			if err := b.Store.SetChatJobState(ctx, job.ID, "blocked", "Requester no longer has access"); err != nil {
				return err
			}
			continue
		}
		var out Outbound
		if err := json.Unmarshal(job.Payload, &out); err != nil {
			return err
		}
		if out.HasAssets {
			out.Assets, err = b.Store.ChatReplyAssets(ctx, job.ID)
			if err != nil {
				return err
			}
			if err := b.prepareReplyAssets(ctx, &out); err != nil {
				if err := b.deliveryFailure(ctx, job, err); err != nil {
					return err
				}
				continue
			}
			allowed, err := b.Store.ChatRequestAllowed(ctx, r)
			if err != nil {
				return err
			}
			if !allowed {
				if err := b.Store.SetChatJobState(ctx, job.ID, "blocked", "Requester no longer has access"); err != nil {
					return err
				}
				continue
			}
		}
		if err := b.send(ctx, job, out); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) prepareReplyAssets(ctx context.Context, out *Outbound) error {
	if b.Upload == nil {
		return nil
	}
	ready := out.Assets[:0]
	for _, asset := range out.Assets {
		if asset.Kind != model.ChatAssetLink {
			key, err := b.Upload(ctx, asset)
			if err != nil {
				var failure *DeliveryError
				if asset.Kind != model.ChatAssetPreview || !errors.As(err, &failure) || failure.Retryable {
					return err
				}
				out.Text += "\n\n有一张图片上传失败。"
				continue
			}
			asset.Key, asset.Data = key, nil
		}
		ready = append(ready, asset)
	}
	out.Assets = ready
	return nil
}

//go:embed team-assistant-prompt.md
var teamAssistantPrompt string

const assistantReplyContract = "Return a JSON object with a single string field summary containing the reply."
const assistantAnswerInstructions = "Handle the sender's request directly and reply in Feishu. For work the sender asks you to do, use the tools allowed by your profile, verify the result when possible, and report what happened."
const assistantRouteInstructions = "Classify this request without using tools. Use team_status when the sender asks for an overview of one team's current state, such as '帮我看下 alpha 的状态'. Choose answer for general questions or work the sender asks you to do yourself. Choose route only when the sender asks you to forward a message or have a team do the work."

func teamAssistantPromptFor(kind string) (field, prompt string) {
	assistant, summary, _ := strings.Cut(teamAssistantPrompt, "\n## Summarize a result\n")
	switch kind {
	case "summary":
		return "summary", strings.TrimSpace(summary) + "\n\n" + assistantReplyContract
	case "answer":
		return "summary", strings.TrimSpace(assistant) + "\n\n" + assistantAnswerInstructions + "\n\n" + assistantReplyContract
	}
	contract := "\n\n" + assistantRouteInstructions + " Output JSON with string fields action, alias, team, leader, subject, owner, description, runtime. " +
		"Actions: " + strings.Join(teamAssistantActions, ", ") +
		". Use an exact existing team alias for an existing team, and empty strings for missing values."
	return "alias", strings.TrimSpace(assistant) + contract
}
