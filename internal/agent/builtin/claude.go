package builtin

import (
	"os"
	"slices"

	"github.com/benlocal/team-worker/internal/agent"
)

// claude is the built-in adapter for Claude Code: hooks deliver team
// messages, and question menus are off because spawned agents run unattended.
func claude() agent.Def {
	return agent.Def{
		Screen: screenRules["claude"],
		Name:   "claude", Names: []string{"claude", "claude-code"},
		SkipArgs: []string{"--dangerously-skip-permissions"},
		// The = form matters: --disallowedTools takes several values and
		// would swallow the prompt that follows as another tool name.
		NoAskArgs:  []string{"--disallowedTools=AskUserQuestion"},
		TmuxPrompt: arg, BatchPrompt: flag("-p"),
		// A leader running inside Claude Code must not make workers think they are nested.
		Env: agent.EnvPlan{Unset: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION"}},
		Startup: []agent.StartupRule{
			{All: []string{"yes, i accept"}, Any: [][]string{{"permission", "dangerously"}}, Keys: []string{"Down", "Enter"}},
			// Newer trust prompt: "❯ No, exit" is preselected, so Enter alone would quit.
			{All: []string{"yes, i trust this folder"}, Any: [][]string{{"❯ no, exit", "> no, exit"}},
				Keys: []string{"Down", "Enter"}},
			{All: []string{"yes, i trust this folder"}, Any: [][]string{{"❯ yes, i trust", "> yes, i trust"}},
				Keys: []string{"Enter"}},
			{Any: [][]string{{"trust this folder", "trust the contents"},
				{"enter to confirm", "press enter", "enter to continue"}}, Keys: []string{"Enter"}},
		},
		Hooks: func(args []string, s agent.Session, _ *agent.Plan) []string {
			return append(args, "--settings", agent.ClaudeHookSettings(s.WorkerBin, s.Team, s.AgentName, s.Leader))
		},
		HooksOn: true,
		Adjust: func(args []string, s agent.Session) []string {
			// Claude Code refuses to skip permissions when running as root.
			if os.Getuid() == 0 {
				return slices.DeleteFunc(args, func(a string) bool { return a == "--dangerously-skip-permissions" })
			}
			return args
		},
	}
}
