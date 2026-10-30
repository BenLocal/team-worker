package builtin

import (
	_ "embed"
	"path/filepath"

	"github.com/benlocal/team-worker/internal/agent"
)

//go:embed pi/team-worker.ts
var piExtensionSource []byte

// piExtension writes the team-worker pi extension and loads it with -e.
func piExtension(args []string, s agent.Session, p *agent.Plan) []string {
	if s.StateDir == "" {
		return args
	}
	path := filepath.Join(s.StateDir, "pi", "team-worker.ts")
	p.Files = append(p.Files, agent.File{Path: path, Content: piExtensionSource})
	return append(args, "-e", path)
}

// pi is the built-in adapter for pi: an extension, not hooks, ties it into the team.
func pi() agent.Def {
	return agent.Def{
		Screen: screenRules["pi"],
		Name:   "pi", Names: []string{"pi"},
		// pi has no tool approvals; --approve trusts project-local pi files
		// (extensions, skills) in the working directory for this run.
		SkipArgs:   []string{"--approve"},
		TmuxPrompt: arg, BatchPrompt: flag("-p"),
		// The extension wakes an idle pi itself; see pi/team-worker.ts.
		Hooks:   piExtension,
		HooksOn: true,
	}
}
