package builtin

import "github.com/benlocal/team-worker/internal/agent"

// qwen is the built-in adapter for Qwen Code, whose prompt is pasted into its terminal.
func qwen() agent.Def {
	return agent.Def{
		Screen: screenRules["qwen"],
		Name:   "qwen", Names: []string{"qwen", "qwen-code"},
		SkipArgs:   []string{"--yolo"},
		TmuxPrompt: paste, BatchPrompt: stdin,
	}
}
