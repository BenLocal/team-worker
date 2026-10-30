package builtin

import (
	"slices"

	"github.com/benlocal/team-worker/internal/agent"
)

// codexSubcommands run Codex without its interactive TUI.
var codexSubcommands = []string{"exec", "e", "review", "resume", "fork", "cloud", "mcp", "mcp-server",
	"app-server", "completion", "sandbox", "debug", "apply", "login", "logout", "features"}

// codex is the built-in adapter for Codex: it has hooks, but they stay
// off by default (see the hooks comment below).
func codex() agent.Def {
	return agent.Def{
		Screen: screenRules["codex"],
		Name:   "codex", Names: []string{"codex", "codex-cli"},
		SkipArgs:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		TmuxPrompt: arg, BatchPrompt: arg,
		Startup: []agent.StartupRule{
			{All: []string{"press enter to continue", "trust the contents of this directory"}, Keys: []string{"Enter"}},
			{All: []string{"press enter to continue", "update available", "skip"}, Keys: []string{"Enter"}},
		},
		// Codex runs only trusted hooks. Hooks passed with -c need
		// --dangerously-bypass-hook-trust, so they stay off until an
		// adapter sets hooks = true (see docs/agents.md).
		Hooks: func(args []string, s agent.Session, _ *agent.Plan) []string {
			return append(append(args, "--dangerously-bypass-hook-trust"),
				agent.CodexHookOverrides(s.WorkerBin, s.Team, s.AgentName, s.Leader)...)
		},
		Adjust: func(args []string, s agent.Session) []string {
			if !s.Interactive && !(len(args) > 1 && slices.Contains(codexSubcommands, args[1])) {
				args = slices.Insert(args, 1, "exec")
			}
			return args
		},
	}
}
