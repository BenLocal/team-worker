package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type assistantAction struct {
	Action      string `json:"action"`
	Alias       string `json:"alias"`
	Team        string `json:"team"`
	Leader      string `json:"leader"`
	Subject     string `json:"subject"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	Runtime     string `json:"runtime"`
}

func (a assistantAction) write() bool {
	return a.Action == "create_team" || a.Action == "create_task"
}

func (a assistantAction) arguments() ([]string, bool) {
	validID := func(value string) bool { return model.ValidateIdentifier(value, "identifier", false) == nil }
	switch a.Action {
	case "list_teams":
		return []string{"team", "discover"}, true
	case "team_status", "list_members", "list_tasks":
		if !validID(a.Team) {
			return nil, false
		}
		switch a.Action {
		case "team_status":
			return []string{"board", "show", "--messages=1", "--", a.Team}, true
		case "list_members":
			return []string{"team", "status", "--", a.Team}, true
		}
		return []string{"task", "list", "--", a.Team}, true
	case "create_team":
		if !validID(a.Team) || !validID(a.Leader) {
			return nil, false
		}
		return []string{"team", "spawn-team", "--agent-name=" + a.Leader, "--", a.Team}, true
	case "create_task":
		if !validID(a.Team) || (a.Owner != "" && !validID(a.Owner)) || strings.TrimSpace(a.Subject) == "" ||
			len(a.Subject) > 240 || len(a.Description) > 1000 || strings.ContainsAny(a.Subject+a.Description, "\x00") {
			return nil, false
		}
		return []string{"task", "create", "--owner=" + a.Owner, "--description=" + a.Description, "--", a.Team, a.Subject}, true
	}
	return nil, false
}

func (b *Bridge) queueAssistantAction(ctx context.Context, r model.ChatRequest, settings model.ChatSettings, candidates map[string]model.ChatBinding, a assistantAction) error {
	reply := func(text string) error {
		return b.Store.FinishChatAssistant(ctx, r, store.ChatJob{}, store.ChatJob{
			ID: r.ID + "-assistant-reply", Kind: "reply", Payload: jobJSON(r.Origin.Reply(text)),
		})
	}
	if a.write() && !store.ChatAssistantWriteAllowed(settings, b.Account, r) {
		return reply("创建团队和派发任务仅对已授权的飞书管理账号开放。")
	}
	binding := candidates[a.Alias]
	requestedTeam := a.Team
	if a.Action == "create_team" {
		a.Team = strings.Join(strings.Fields(a.Team), "-")
		if a.Leader == "" {
			a.Leader = "leader"
		}
	} else {
		a.Team = binding.Team
	}
	args, ok := a.arguments()
	if !ok {
		if a.Action == "create_team" {
			return reply("请给出新团队的名称。团队名可用英文字母、数字、点、下划线和连字符，例如 test-team。")
		}
		return reply("请重新发送请求并明确团队名称。派发任务时请附上任务标题，可指定负责人和说明。")
	}
	job := store.ChatJob{ID: r.ID + "-assistant", Kind: "assistant-read", BindingID: binding.BindingID,
		RequestID: r.ID, Payload: jobJSON(a), QueuedAt: b.Store.Now().Unix()}
	if !a.write() {
		return b.Store.FinishChatAssistant(ctx, r, job, store.ChatJob{})
	}
	job.ID, job.Kind = rand.Text(), "assistant-confirm"
	preview := fmt.Sprintf("待确认操作\nteam-worker %s\n\n请在本会话于 10 分钟内回复 /tw confirm %s", jobJSON(args), job.ID)
	if a.Action == "create_team" && a.Team != requestedTeam {
		preview = fmt.Sprintf("团队名里的空格会换成连字符，使用 %s。\n\n%s", a.Team, preview)
	}
	return b.Store.FinishChatAssistant(ctx, r, job, store.ChatJob{
		ID: r.ID + "-confirm", Kind: "reply", Payload: jobJSON(r.Origin.Reply(preview)),
	})
}

func (b *Bridge) runAssistantJobs(ctx context.Context, kind string) error {
	jobs, err := b.Store.PendingChatJobs(ctx, b.accountKey(), kind, "")
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := b.runAssistantJob(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) runAssistantJob(ctx context.Context, job store.ChatJob) error {
	r, err := b.Store.ChatRequest(ctx, job.RequestID)
	if err != nil {
		return err
	}
	var a assistantAction
	if err := json.Unmarshal(job.Payload, &a); err != nil {
		return err
	}
	settings, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	bindings, err := b.Store.ChatBindingsForSender(ctx, settings, b.Account, r.Origin.Sender)
	if err != nil {
		return err
	}
	binding := bindings[a.Alias]
	allowed := (a.Action == "list_teams" && (len(bindings) > 0 || settings.AllowsFeishuSender(b.Account, r.Origin.Sender))) ||
		a.Action == "create_team" || (binding.BindingID != "" && job.BindingID == binding.BindingID && a.Team == binding.Team)
	if r.State != "answered" || !allowed || (a.write() && !store.ChatAssistantWriteAllowed(settings, b.Account, r)) {
		return b.Store.SetChatJobState(ctx, job.ID, "blocked", "Requester no longer has access")
	}
	args, valid := a.arguments()
	if !valid || a.write() != (job.Kind == "assistant-write") {
		return b.Store.SetChatJobState(ctx, job.ID, "failed", "Invalid assistant action")
	}
	if a.write() {
		if a.Action == "create_task" && a.Owner != "" {
			team, err := b.Store.GetTeam(ctx, a.Team)
			if err != nil && store.KindOf(err) != store.KindNotFound {
				return err
			}
			if !slices.ContainsFunc(team.Members, func(m model.Member) bool { return m.InboxName() == a.Owner }) {
				if err := b.Store.ReplaceChatJob(ctx, store.ChatJob{ID: job.ID, Kind: "reply",
					Payload: jobJSON(r.Origin.Reply("负责人不在该团队中。请核对成员名称后重新提交。"))}); err != nil {
					return err
				}
				return nil
			}
		}
		claimed, err := b.Store.ClaimChatAction(ctx, job.ID, settings.Revision)
		if err != nil || !claimed {
			return err
		}
	}
	var output []byte
	err = errors.New("Team worker CLI is unavailable")
	if b.RunCLI != nil {
		output, err = b.RunCLI(ctx, args)
	}
	text := "查询失败，请稍后重试。"
	if a.write() {
		text = "操作未返回成功结果。请先查询实际状态，再决定是否重新提交。"
	}
	if err == nil {
		text = a.result(output, bindings, text)
	}
	if a.write() {
		return b.Store.CompleteChatAction(ctx, job.ID, r.Origin.Reply(text))
	}
	latest, err := b.Store.ChatSettings(ctx)
	if err != nil {
		return err
	}
	if latest.Revision != settings.Revision {
		return nil
	}
	return b.send(ctx, job, r.Origin.Reply(text))
}

func (a assistantAction) result(output []byte, bindings map[string]model.ChatBinding, fallback string) string {
	switch a.Action {
	case "team_status":
		var board model.Board
		if json.Unmarshal(output, &board) != nil || board.Team.Name != a.Team {
			return fallback
		}
		return summarizeTeamStatus(board)
	case "list_teams":
		var teams []model.TeamSummary
		if json.Unmarshal(output, &teams) != nil {
			return fallback
		}
		aliases := make(map[string]string, len(bindings))
		for alias, binding := range bindings {
			aliases[binding.Team] = alias
		}
		var names []string
		for _, team := range teams {
			if alias, visible := aliases[team.Name]; visible {
				names = append(names, alias)
			}
		}
		slices.Sort(names)
		return assistantList("团队", names)
	case "list_members":
		var team model.Team
		if json.Unmarshal(output, &team) != nil || team.Name != a.Team {
			return fallback
		}
		var names []string
		for _, member := range team.Members {
			names = append(names, member.InboxName())
		}
		return assistantList("成员", names)
	case "list_tasks":
		var tasks []model.Task
		if json.Unmarshal(output, &tasks) != nil {
			return fallback
		}
		var names []string
		for _, task := range tasks[:min(20, len(tasks))] {
			subject := []rune(task.Subject)
			names = append(names, fmt.Sprintf("%s [%s] %s", task.ID, task.Status, string(subject[:min(120, len(subject))])))
		}
		return assistantListText("任务", len(tasks), names)
	case "create_team":
		var team struct{ Team, Status string }
		if json.Unmarshal(output, &team) == nil && team.Team == a.Team && team.Status == "created" {
			return fmt.Sprintf("已创建团队 %s，Leader 为 %s。", a.Team, a.Leader)
		}
	case "create_task":
		var task model.Task
		if json.Unmarshal(output, &task) == nil && task.ID != "" {
			text := fmt.Sprintf("已在 %s 创建任务 %s：%s", a.Team, task.ID, task.Subject)
			if task.Owner != "" {
				text += "\n负责人 " + task.Owner
			}
			return text
		}
	}
	return fallback
}

func assistantList(label string, items []string) string {
	return assistantListText(label, len(items), items[:min(20, len(items))])
}

func assistantListText(label string, count int, items []string) string {
	text := fmt.Sprintf("当前有 %d 个%s。", count, label)
	if len(items) > 0 {
		text += "\n" + strings.Join(items, "\n")
	}
	if count > len(items) {
		text += "\n仅显示前 20 项。"
	}
	return text
}

func assistantCLI(dataDir, socket string) func(context.Context, []string) ([]byte, error) {
	binary := "team-worker"
	if exe, err := os.Executable(); err == nil {
		if sibling, err := exec.LookPath(filepath.Join(filepath.Dir(exe), binary)); err == nil {
			binary = sibling
		}
	}
	return func(ctx context.Context, args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--json"}, args...)...)
		cmd.Env = []string{"TEAM_WORKER_DATA_DIR=" + dataDir, "TEAM_WORKER_SOCKET=" + socket,
			"TEAM_WORKER_SERVER_URL=", "TEAM_WORKER_NO_AUTOSTART=1"}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		defer stdout.Close()
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		var output bytes.Buffer
		const limit = 8 << 20
		n, err := io.Copy(&output, io.LimitReader(stdout, limit+1))
		if n > limit || err != nil {
			cancel()
			err = errors.Join(err, errors.New("Team worker CLI output could not be read"))
		}
		return output.Bytes(), errors.Join(err, cmd.Wait())
	}
}
