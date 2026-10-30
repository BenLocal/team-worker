package builtin

import (
	"testing"

	"github.com/benlocal/team-worker/internal/agent"
)

// Screens below were captured from real sessions (trailing spaces trimmed,
// rows joined with "\n" and ending in "\n", as the daemon prepares them).
const (
	claudeIdle = "● NO_ASK_TOOL\n\n✻ Cooked for 7s · done 9:12 PM\n" +
		"────────────────────────────────────────\n❯\n────────────────────────────────────────\n" +
		"  ~/.c/t/ws master\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
	claudeWorking = "● 会话重启后已恢复（10:16）\n✢ Lollygagging… (Waiting for team messages and tasks...… · 28s · ↓ 232 tokens)\n" +
		"────────────────────────────────────────\n❯\n────────────────────────────────────────\n" +
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt\n"
	claudePermission = " team-worker task list e2e --owner carol\n This command requires approval\n Do you want to proceed?\n" +
		" ❯ 1. Yes\n   2. Yes, and don’t ask again for: team-worker task *\n   4. No\n Esc to cancel · Tab to amend\n"
	claudeQuestion = " ☐ Continue\nContinue?\n❯ 1. Yes\n     Proceed.\n  2. No\n     Stop.\n  3. Type something.\n" +
		"────────────────────────────────────────\n  4. Chat about this\nEnter to select · ↑/↓ to navigate · Esc to cancel\n"
	// A reply ending in a numbered list fooled the old screen check.
	claudeIdleAfterList = "  待用户决定的事项\n  1. 114:8087 的凭据缺失\n  2. 是否授权安装 make\n✻ Churned for 3m 16s · done 4:17 AM\n" +
		"────────────────────────────────────────\n❯\n────────────────────────────────────────\n  ⏵⏵ auto mode on (shift+tab to cycle)\n"
)

func TestScreenRulesJudgeRealScreens(t *testing.T) {
	r := Registry()
	cases := []struct {
		name, cli, screen, title string
		want                     agent.ScreenState
		visible                  bool // Visible flag for the wanted state
	}{
		{"claude at its prompt", "claude", claudeIdle, "", agent.ScreenIdle, true},
		{"claude after a numbered reply", "claude", claudeIdleAfterList, "", agent.ScreenIdle, true},
		{"claude mid-turn", "claude", claudeWorking, "", agent.ScreenWorking, true},
		{"claude permission prompt", "claude", claudePermission, "", agent.ScreenBlocked, true},
		{"claude question menu", "claude", claudeQuestion, "", agent.ScreenBlocked, true},
		// The title spinner outranks the screen, as in herdr.
		{"claude title spinner", "claude", claudeIdle, "◐ courselab 团队迁移后恢复", agent.ScreenWorking, true},
		{"claude title at rest", "claude", "", "✳ courselab 需求分诊接入", agent.ScreenIdle, true},
		{"codex title spinner", "codex", "", "⠋ 审核课程实验后端代码仓库 | reviewer", agent.ScreenWorking, true},
		{"codex title at rest", "codex", "", "审核课程实验后端代码仓库 | reviewer", agent.ScreenIdle, true},
		// Codex draws an approval below the last prompt line it echoed.
		{"codex approval", "codex", "› list the files\n\n• Running ls -la\n\nAllow command?\n  ls -la\n" +
			"  Press enter to confirm or esc to cancel\n", "", agent.ScreenBlocked, true},
	}
	for _, c := range cases {
		d, ok := r.For([]string{c.cli}).DetectScreen(agent.ScreenInput{Screen: c.screen, Title: c.title})
		visible := map[agent.ScreenState]bool{agent.ScreenIdle: d.VisibleIdle, agent.ScreenWorking: d.VisibleWorking,
			agent.ScreenBlocked: d.VisibleBlocker}[c.want]
		if !ok || d.State != c.want || visible != c.visible {
			t.Errorf("%s: got %+v (ok=%v), want %s visible=%v", c.name, d, ok, c.want, c.visible)
		}
	}
}

func TestEveryBuiltinWithRulesHasThem(t *testing.T) {
	r := Registry()
	for _, d := range Defs() {
		_, ok := r.For(d.Names[:1]).DetectScreen(agent.ScreenInput{})
		if want := d.Name != "dsh"; ok != want {
			t.Errorf("%s: screen rules present = %v, want %v", d.Name, ok, want)
		}
	}
	// Without a rule match the fallback is idle but carries no evidence.
	d, _ := r.For([]string{"claude"}).DetectScreen(agent.ScreenInput{Screen: "hello\n"})
	if d.State != agent.ScreenIdle || d.Rule != "" || d.VisibleIdle {
		t.Errorf("fallback: %+v", d)
	}
	if _, ok := r.For([]string{"my-agent"}).DetectScreen(agent.ScreenInput{}); ok {
		t.Error("the generic adapter has no screen rules")
	}
}
