package builtin

import "github.com/benlocal/team-worker/internal/agent"

// gemini is the built-in adapter for Gemini CLI.
func gemini() agent.Def {
	return agent.Def{
		Screen: screenRules["gemini"],
		Name:   "gemini", Names: []string{"gemini"},
		SkipArgs:   []string{"--yolo"},
		TmuxPrompt: flag("-i"), BatchPrompt: flag("-p"),
		Startup: []agent.StartupRule{{Any: [][]string{{"trust folder", "trust parent folder"}}, Keys: []string{"Enter"}}},
	}
}
