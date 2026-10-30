package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Hook timing. The Stop hook waits for new work this long before letting
// the agent go idle; Claude Code's hook timeout must exceed it.
const (
	WorkerStopWait = 300
	LeaderStopWait = 600
)

// hookCommands are the team-worker hook commands for one agent, by event.
func hookCommands(workerBin, team, agentName string, leader bool) (cmds map[string]string, stopTimeout int) {
	role, wait := "worker", WorkerStopWait
	if leader {
		role, wait = "leader", LeaderStopWait
	}
	cmds = map[string]string{}
	for _, event := range []string{"stop", "post-tool-use", "session-start", "prompt-submit", "notification"} {
		cmds[event] = strings.Join([]string{shellQuote(workerBin), "hook", event, "--team", shellQuote(team),
			"--agent", shellQuote(agentName), "--role", role}, " ")
	}
	return cmds, wait + 60
}

// CodexHookOverrides are `-c` arguments declaring the same hooks for Codex,
// whose hooks use the Claude Code event names and JSON protocol.
func CodexHookOverrides(workerBin, team, agentName string, leader bool) []string {
	cmds, stopTimeout := hookCommands(workerBin, team, agentName, leader)
	entry := func(matcher, cmd string, timeout int) string {
		m := ""
		if matcher != "" {
			m = "matcher=" + tomlString(matcher) + ","
		}
		return fmt.Sprintf("[{%shooks=[{type=\"command\",command=%s,timeout=%d}]}]", m, tomlString(cmd), timeout)
	}
	return []string{
		"-c", "hooks.Stop=" + entry("", cmds["stop"], stopTimeout),
		"-c", "hooks.PostToolUse=" + entry("*", cmds["post-tool-use"], 15),
		"-c", "hooks.SessionStart=" + entry("startup|resume|clear|compact", cmds["session-start"], 15),
	}
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ClaudeHookSettings is the Claude Code settings JSON, passed with
// --settings, that routes Stop, PostToolUse, SessionStart, UserPromptSubmit
// and Notification to `team-worker hook`. The last two only report whether
// the agent works, waits at its prompt or asks the user something. It only
// adds hooks; the user's settings still apply.
func ClaudeHookSettings(workerBin, team, agentName string, leader bool) string {
	cmds, stopTimeout := hookCommands(workerBin, team, agentName, leader)
	cmd := func(event string) string { return cmds[event] }
	type hook struct {
		Type          string `json:"type"`
		Command       string `json:"command"`
		Timeout       int    `json:"timeout"`
		StatusMessage string `json:"statusMessage,omitempty"`
	}
	type matcher struct {
		Matcher string `json:"matcher,omitempty"`
		Hooks   []hook `json:"hooks"`
	}
	settings := map[string]map[string][]matcher{"hooks": {
		"Stop": {{Hooks: []hook{{Type: "command", Command: cmd("stop"), Timeout: stopTimeout,
			StatusMessage: "Waiting for team messages and tasks..."}}}},
		"PostToolUse":      {{Matcher: "*", Hooks: []hook{{Type: "command", Command: cmd("post-tool-use"), Timeout: 15}}}},
		"SessionStart":     {{Matcher: "startup|resume|clear|compact", Hooks: []hook{{Type: "command", Command: cmd("session-start"), Timeout: 15}}}},
		"UserPromptSubmit": {{Hooks: []hook{{Type: "command", Command: cmd("prompt-submit"), Timeout: 15}}}},
		"Notification": {
			{Matcher: "idle_prompt", Hooks: []hook{{Type: "command", Command: cmd("notification") + " --activity waiting", Timeout: 15}}},
			{Matcher: "permission_prompt|elicitation_dialog", Hooks: []hook{{Type: "command", Command: cmd("notification") + " --activity blocked", Timeout: 15}}},
		},
	}}
	data, _ := json.Marshal(settings)
	return string(data)
}
