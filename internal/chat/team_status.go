package chat

import (
	"fmt"
	"slices"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

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
	fmt.Fprintf(&text, "%s 当前状态\n成员在线 %d/%d", board.Team.Name, online, len(board.Members))
	if unknown > 0 {
		fmt.Fprintf(&text, "，%d 人状态未知", unknown)
	}
	stats := board.Stats
	fmt.Fprintf(&text, "\n任务进行中 %d，等待或未报进展 %d，阻塞 %d，待开始 %d。", stats.InProgress-stats.Waiting, stats.Waiting, stats.Blocked, stats.Pending)

	var running, waiting, blocked []model.Task
	var completed model.Task
	for _, task := range board.Tasks {
		if task.Kind == "goal" {
			continue
		}
		switch task.Status {
		case model.StatusInProgress:
			if task.ExecutionState == "waiting" || task.ExecutionState == "unregistered" {
				waiting = append(waiting, task)
			} else {
				running = append(running, task)
			}
		case model.StatusBlocked:
			blocked = append(blocked, task)
		case model.StatusCompleted:
			if completed.Subject == "" || task.UpdatedAt > completed.UpdatedAt {
				completed = task
			}
		}
	}
	appendTeamStatusTasks(&text, "正在做", running)
	appendTeamStatusTasks(&text, "等待处理", waiting)
	appendTeamStatusTasks(&text, "阻塞", blocked)
	if completed.Subject != "" {
		fmt.Fprintf(&text, "\n\n最近一次完成\n- %s", teamStatusLine(completed.Subject, 120))
		if len(completed.UpdatedAt) >= 10 {
			fmt.Fprintf(&text, "（%s）", completed.UpdatedAt[:10])
		}
	}
	return text.String()
}

func appendTeamStatusTasks(text *strings.Builder, heading string, tasks []model.Task) {
	if len(tasks) == 0 {
		return
	}
	slices.SortStableFunc(tasks, func(a, b model.Task) int {
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
		detail := ""
		if len(task.Progress) > 0 {
			latest := task.Progress[len(task.Progress)-1]
			detail = latest.Summary
			if task.ExecutionState == "waiting" || task.Status == model.StatusBlocked {
				detail = latest.Reason
			}
		}
		if detail == "" && len(task.BlockedBy) > 0 {
			detail = fmt.Sprintf("还有 %d 个依赖未完成", len(task.BlockedBy))
		}
		if detail != "" {
			fmt.Fprintf(text, "。%s", teamStatusLine(detail, 140))
		}
	}
	if len(tasks) > limit {
		fmt.Fprintf(text, "\n另有 %d 项。", len(tasks)-limit)
	}
}

func teamStatusLine(text string, limit int) string {
	line := []rune(strings.Join(strings.Fields(text), " "))
	if len(line) > limit {
		return string(line[:limit]) + "…"
	}
	return string(line)
}
