// Package builtin holds team-worker's built-in agent adapters, one file per
// CLI. The agent package knows how adapters behave; this package says which
// CLIs exist and how each one is started.
package builtin

import (
	"embed"
	"path"

	"github.com/benlocal/team-worker/internal/agent"
)

// detectFiles are herdr's agent detection manifests (see detect/NOTICE.md).
//
//go:embed detect/*.toml
var detectFiles embed.FS

// screenRules parses a built-in manifest once; an invalid one fails the
// tests and panics at startup rather than misjudging agents later.
var screenRules = func() map[string]*agent.ScreenRules {
	entries, err := detectFiles.ReadDir("detect")
	if err != nil {
		panic(err)
	}
	rules := map[string]*agent.ScreenRules{}
	for _, e := range entries {
		data, err := detectFiles.ReadFile(path.Join("detect", e.Name()))
		if err != nil {
			panic(err)
		}
		rules[e.Name()[:len(e.Name())-len(".toml")]] = agent.MustParseScreenRules(e.Name(), data)
	}
	return rules
}()

var (
	arg   = agent.PromptMode{Kind: "arg"}
	paste = agent.PromptMode{Kind: "paste"}
	stdin = agent.PromptMode{Kind: "stdin"}
)

func flag(flags ...string) agent.PromptMode { return agent.PromptMode{Kind: "flag", Flags: flags} }

// Defs lists the built-in adapters.
func Defs() []agent.Def {
	return []agent.Def{claude(), codex(), gemini(), kimi(), qwen(), opencode(), dsh(), pi()}
}

// Registry returns a registry with only the built-in adapters.
func Registry() *agent.Registry { return agent.NewRegistry(Defs()) }

// Load reads every *.toml adapter in dir on top of the built-in adapters. A
// missing directory is not an error.
func Load(dir string) (*agent.Registry, error) { return agent.LoadDir(Defs(), dir) }
