package builtin

import "github.com/benlocal/team-worker/internal/agent"

// opencode is the built-in adapter for opencode, whose prompt is pasted into its terminal.
func opencode() agent.Def {
	return agent.Def{
		Screen: screenRules["opencode"],
		Name:   "opencode", Names: []string{"opencode"},
		SkipArgs:   []string{"--yolo"},
		TmuxPrompt: paste, BatchPrompt: stdin,
	}
}
