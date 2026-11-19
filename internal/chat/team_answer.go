package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (b *Bridge) answerAssistantQuestion(ctx context.Context, request model.ChatRequest, job store.ChatJob, settings model.ChatSettings,
	teams map[string]model.ChatBinding, alias string) error {
	input := agentInput{Request: request.Body, Teams: slices.Sorted(maps.Keys(teams)), SessionKey: assistantSessionKey(request, settings.Revision),
		Scoped: !settings.AllowsFeishuSender(b.Account, request.Origin.Sender)}
	if binding, ok := teams[alias]; ok && b.RunCLI != nil {
		snapshot := assistantAction{Action: "team_status", Team: binding.Team}
		if args, valid := snapshot.arguments(); valid {
			var board model.Board
			output, err := b.RunCLI(ctx, args)
			if err == nil && json.Unmarshal(output, &board) == nil && board.Team.Name == binding.Team {
				input.Context = teamAnswerContext(board)
			}
		}
	}
	// The board and candidate list are readable only under the settings used for this turn.
	latest, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	if latest.Revision != settings.Revision {
		return b.Store.RetryChatJob(ctx, job)
	}
	claimed, err := b.Store.ClaimChatDelivery(ctx, job.ID)
	if err != nil || !claimed {
		return err
	}
	if !input.Scoped && b.Account.Provider == "feishu" && b.Deliver != nil {
		input.interact = func(ctx context.Context, interaction codexInteraction) (any, error) {
			return b.waitForCodexCard(ctx, request.Origin, settings.Revision, interaction)
		}
	}
	answer, err := b.Agent(ctx, "answer", input)
	text := strings.TrimSpace(answer.Summary)
	if err != nil || text == "" || len(text) > 16000 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		text = assistantFailureText(err)
	}
	return b.finishAssistantAnswer(ctx, request, settings.Revision, text)
}

func assistantFailureText(err error) string {
	if errors.Is(err, errCodexCardExpired) {
		return "这次 Codex 交互等待超时，回合已结束。"
	}
	if errors.Is(err, errCodexInteraction) {
		return "这次交互无法在飞书完成，Codex 回合已结束。"
	}
	return "这次没有收到完整的答复。如果请求涉及操作，请先查实际结果再重试。"
}

func (b *Bridge) finishAssistantAnswer(ctx context.Context, request model.ChatRequest, revision int64, text string) error {
	reply := request.Origin.Reply(text)
	return b.Store.FinishChatAssistant(ctx, request, store.ChatJob{}, store.ChatJob{
		ID: request.ID + "-answer", Kind: "assistant-answer", RequestID: request.ID,
		BindingID: strconv.FormatInt(revision, 10), Payload: jobJSON(reply),
	})
}

func teamAnswerContext(board model.Board) string {
	var text strings.Builder
	text.WriteString(summarizeTeamStatus(board, false))
	fmt.Fprintf(&text, "\n\n成员（最多 20 项）\n")
	for _, member := range board.Members[:min(20, len(board.Members))] {
		fmt.Fprintf(&text, "- %s (%s)\n", member.InboxName(), member.AgentType)
	}
	var completed []model.Task
	for _, task := range board.Tasks {
		if task.Kind != "goal" && task.Status == model.StatusCompleted {
			completed = append(completed, task)
		}
	}
	slices.SortStableFunc(completed, func(a, b model.Task) int {
		return strings.Compare(taskCompletionTime(b), taskCompletionTime(a))
	})
	if len(completed) > 0 {
		text.WriteString("\n最近完成（最多 5 项）\n")
	}
	for _, task := range completed[:min(5, len(completed))] {
		fmt.Fprintf(&text, "- %s %s\n", task.ID, teamStatusLine(task.Subject, 120))
		if result := task.Result; result != nil {
			fmt.Fprintf(&text, "  结果 %s\n  验证 %s\n",
				teamStatusLine(result.Summary, 180), teamStatusLine(result.Verification, 120))
		}
	}
	return text.String()
}
