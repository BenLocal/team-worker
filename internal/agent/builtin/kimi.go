package builtin

import "github.com/benlocal/team-worker/internal/agent"

// kimi is the built-in adapter for Kimi CLI, which takes its prompt with --print.
func kimi() agent.Def {
	return agent.Def{
		Screen: screenRules["kimi"],
		Name:   "kimi", Names: []string{"kimi"},
		SkipArgs:   []string{"--yolo"},
		TmuxPrompt: flag("--print", "-p"), BatchPrompt: flag("--print", "-p"),
	}
}
