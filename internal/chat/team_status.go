package chat

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

var directTeamStatus = regexp.MustCompile(`(?i)^(?:请\s*)?(?:帮我\s*)?(?:(?:看下|看一下|看看|查看|查下|查一下)\s*)?([a-z0-9._-]+)\s*(?:(?:team|团队)\s*)?(?:的\s*)?(?:(?:当前|现在)\s*的?\s*)?状态\s*[？?。]?$`)

func directTeamStatusAlias(text string, teams map[string]model.ChatBinding) string {
	match := directTeamStatus.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return ""
	}
	if _, ok := teams[match[1]]; ok {
		return match[1]
	}
	alias := strings.ToLower(match[1])
	if _, ok := teams[alias]; ok {
		return alias
	}
	return ""
}

func summarizeTeamStatus(board model.Board) string {
	online, unknown := 0, 0
	for _, member := range board.Members {
		if member.Agent == nil {
			continue
		}
		if member.Agent.Alive == nil {
			unknown++
		} else if *member.Agent.Alive {
			online++
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s 当前状态\n进程在线 %d/%d", board.Team.Name, online, len(board.Members))
	if unknown > 0 {
		fmt.Fprintf(&text, "，%d 人状态未知", unknown)
	}
	var active, followUp, waiting, unreported, blocked []model.Task
	var completed model.Task
	for _, task := range board.Tasks {
		if task.Kind == "goal" {
			continue
		}
		switch task.Status {
		case model.StatusInProgress:
			switch task.ExecutionState {
			case "active":
				active = append(active, task)
			case "follow_up":
				followUp = append(followUp, task)
			case "waiting":
				waiting = append(waiting, task)
			default:
				unreported = append(unreported, task)
			}
		case model.StatusBlocked:
			blocked = append(blocked, task)
		case model.StatusCompleted:
			if completed.Subject == "" || taskCompletionTime(task) > taskCompletionTime(completed) {
				completed = task
			}
		}
	}
	fmt.Fprintf(&text, "\n执行中 %d，待跟进 %d，等待 %d，未报进展或未登记等待 %d，阻塞 %d，待开始 %d。",
		len(active), len(followUp), len(waiting), len(unreported), len(blocked), board.Stats.Pending)
	appendTeamStatusTasks(&text, "执行中", active)
	appendTeamStatusTasks(&text, "待跟进", followUp)
	appendTeamStatusTasks(&text, "等待处理", waiting)
	appendTeamStatusTasks(&text, "未报进展或未登记等待", unreported)
	appendTeamStatusTasks(&text, "阻塞", blocked)
	if completed.Subject != "" {
		fmt.Fprintf(&text, "\n\n最近一次完成\n- %s", teamStatusLine(completed.Subject, 120))
		if timestamp := taskCompletionTime(completed); len(timestamp) >= 10 {
			fmt.Fprintf(&text, "（%s）", timestamp[:10])
		}
	}
	return text.String()
}

func taskCompletionTime(task model.Task) string {
	if task.Result != nil && task.Result.ReportedAt != "" {
		return task.Result.ReportedAt
	}
	return task.UpdatedAt
}

func appendTeamStatusTasks(text *strings.Builder, heading string, tasks []model.Task) {
	if len(tasks) == 0 {
		return
	}
	slices.SortStableFunc(tasks, func(a, b model.Task) int {
		if aAck, bAck := acknowledgedWait(a), acknowledgedWait(b); aAck != bAck {
			if aAck {
				return -1
			}
			return 1
		}
		return strings.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	fmt.Fprintf(text, "\n\n%s\n", heading)
	const limit = 3
	for i, task := range tasks[:min(limit, len(tasks))] {
		if i > 0 {
			text.WriteByte('\n')
		}
		fmt.Fprintf(text, "- %s", teamStatusLine(task.Subject, 120))
		if task.Owner != "" {
			fmt.Fprintf(text, "（%s）", task.Owner)
		}
		var progress model.TaskProgress
		if len(task.Progress) > 0 {
			progress = task.Progress[len(task.Progress)-1]
		}
		detail := progress.Summary
		switch {
		case acknowledgedWait(task):
			detail = ""
		case task.ExecutionState == "waiting" || task.Status == model.StatusBlocked:
			detail = progress.Reason
		case task.ExecutionState == "follow_up" && task.WaitCheckpoint != nil:
			detail = task.WaitCheckpoint.Summary
		}
		if detail == "" && len(task.BlockedBy) > 0 {
			detail = fmt.Sprintf("还有 %d 个依赖未完成", len(task.BlockedBy))
		}
		if detail != "" {
			fmt.Fprintf(text, "。%s", teamStatusLine(detail, 140))
		}
		checkpoint := task.NextCheckpoint
		if task.ExecutionState == "waiting" && task.WaitCheckpoint != nil {
			checkpoint = task.WaitCheckpoint
		}
		if checkpoint != nil && !checkpoint.DueAt.IsZero() {
			fmt.Fprintf(text, "。检查点 %s", checkpoint.DueAt.UTC().Format("01-02 15:04 UTC"))
			if checkpoint.Status == "acknowledged" {
				text.WriteString(" 已确认，待提交结果")
			}
			if checkpoint.Action != "" {
				fmt.Fprintf(text, "，%s", teamStatusLine(checkpoint.Action, 100))
			}
		} else if task.ExecutionState == "active" && progress.NextAction != "" {
			fmt.Fprintf(text, "。下一步 %s", teamStatusLine(progress.NextAction, 100))
		}
	}
	if len(tasks) > limit {
		fmt.Fprintf(text, "\n另有 %d 项。", len(tasks)-limit)
	}
}

func acknowledgedWait(task model.Task) bool {
	return task.ExecutionState == "waiting" && task.WaitCheckpoint != nil && task.WaitCheckpoint.Status == "acknowledged"
}

func teamStatusLine(text string, limit int) string {
	line := []rune(strings.Join(strings.Fields(text), " "))
	if len(line) > limit {
		return string(line[:limit]) + "…"
	}
	return string(line)
}
