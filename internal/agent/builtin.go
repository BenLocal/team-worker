package agent

import (
	_ "embed"
	"os"
	"path/filepath"
	"slices"
)

//go:embed pi/team-worker.ts
var piExtensionSource []byte

// piExtension writes the team-worker pi extension and loads it with -e.
func piExtension(args []string, s Session, p *Plan) []string {
	if s.StateDir == "" {
		return args
	}
	path := filepath.Join(s.StateDir, "pi", "team-worker.ts")
	p.Files = append(p.Files, File{Path: path, Content: piExtensionSource})
	return append(args, "-e", path)
}

var (
	arg   = PromptMode{Kind: "arg"}
	paste = PromptMode{Kind: "paste"}
	stdin = PromptMode{Kind: "stdin"}
)

func flag(flags ...string) PromptMode { return PromptMode{Kind: "flag", Flags: flags} }

// codexSubcommands run Codex without its interactive TUI.
var codexSubcommands = []string{"exec", "e", "review", "resume", "fork", "cloud", "mcp", "mcp-server",
	"app-server", "completion", "sandbox", "debug", "apply", "login", "logout", "features"}

func builtins() []*cli {
	return []*cli{
		{
			name: "claude", names: []string{"claude", "claude-code"},
			skipArgs: []string{"--dangerously-skip-permissions"},
			// The = form matters: --disallowedTools takes several values and
			// would swallow the prompt that follows as another tool name.
			noAskArgs:  []string{"--disallowedTools=AskUserQuestion"},
			tmuxPrompt: arg, batchPrompt: flag("-p"),
			// A leader running inside Claude Code must not make workers think they are nested.
			env: EnvPlan{Unset: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION"}},
			startup: []StartupRule{
				{All: []string{"yes, i accept"}, Any: [][]string{{"permission", "dangerously"}}, Keys: []string{"Down", "Enter"}},
				// Newer trust prompt: "❯ No, exit" is preselected, so Enter alone would quit.
				{All: []string{"yes, i trust this folder"}, Any: [][]string{{"❯ no, exit", "> no, exit"}},
					Keys: []string{"Down", "Enter"}},
				{All: []string{"yes, i trust this folder"}, Any: [][]string{{"❯ yes, i trust", "> yes, i trust"}},
					Keys: []string{"Enter"}},
				{Any: [][]string{{"trust this folder", "trust the contents"},
					{"enter to confirm", "press enter", "enter to continue"}}, Keys: []string{"Enter"}},
			},
			hooks: func(args []string, s Session, _ *Plan) []string {
				return append(args, "--settings", ClaudeHookSettings(s.WorkerBin, s.Team, s.AgentName, s.Leader))
			},
			hooksOn: true,
			adjust: func(args []string, s Session) []string {
				// Claude Code refuses to skip permissions when running as root.
				if os.Getuid() == 0 {
					return slices.DeleteFunc(args, func(a string) bool { return a == "--dangerously-skip-permissions" })
				}
				return args
			},
		},
		{
			name: "codex", names: []string{"codex", "codex-cli"},
			skipArgs:   []string{"--dangerously-bypass-approvals-and-sandbox"},
			tmuxPrompt: arg, batchPrompt: arg,
			startup: []StartupRule{
				{All: []string{"press enter to continue", "trust the contents of this directory"}, Keys: []string{"Enter"}},
				{All: []string{"press enter to continue", "update available", "skip"}, Keys: []string{"Enter"}},
			},
			// Codex runs only trusted hooks. Hooks passed with -c need
			// --dangerously-bypass-hook-trust, so they stay off until an
			// adapter sets hooks = true (see docs/agents.md).
			hooks: func(args []string, s Session, _ *Plan) []string {
				return append(append(args, "--dangerously-bypass-hook-trust"),
					CodexHookOverrides(s.WorkerBin, s.Team, s.AgentName, s.Leader)...)
			},
			adjust: func(args []string, s Session) []string {
				if !s.Interactive && !(len(args) > 1 && slices.Contains(codexSubcommands, args[1])) {
					args = slices.Insert(args, 1, "exec")
				}
				return args
			},
		},
		{
			name: "gemini", names: []string{"gemini"},
			skipArgs:   []string{"--yolo"},
			tmuxPrompt: flag("-i"), batchPrompt: flag("-p"),
			startup: []StartupRule{{Any: [][]string{{"trust folder", "trust parent folder"}}, Keys: []string{"Enter"}}},
		},
		{
			name: "kimi", names: []string{"kimi"},
			skipArgs:   []string{"--yolo"},
			tmuxPrompt: flag("--print", "-p"), batchPrompt: flag("--print", "-p"),
		},
		{
			name: "qwen", names: []string{"qwen", "qwen-code"},
			skipArgs:   []string{"--yolo"},
			tmuxPrompt: paste, batchPrompt: stdin,
		},
		{
			name: "opencode", names: []string{"opencode"},
			skipArgs:   []string{"--yolo"},
			tmuxPrompt: paste, batchPrompt: stdin,
		},
		{
			name: "dsh", names: []string{"dsh"},
			// dsh has no bypass flag; this pins the permission mode for the session.
			skipEnv:    map[string]string{"DSH_PERMISSION_MODE": "danger-full-access"},
			tmuxPrompt: arg, batchPrompt: arg,
		},
		{
			name: "pi", names: []string{"pi"},
			// pi has no tool approvals; --approve trusts project-local pi files
			// (extensions, skills) in the working directory for this run.
			skipArgs:   []string{"--approve"},
			tmuxPrompt: arg, batchPrompt: flag("-p"),
			// The extension wakes an idle pi itself; see pi/team-worker.ts.
			hooks:   piExtension,
			hooksOn: true,
		},
	}
}

// generic handles any other CLI: no permission flags, prompt pasted into
// the terminal or fed on stdin.
func generic() *cli {
	return &cli{name: "generic", source: "builtin", tmuxPrompt: paste, batchPrompt: stdin}
}
