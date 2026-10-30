// Package agent knows how each agent CLI is started: its permission flags,
// how it takes the first prompt, the environment it needs and the startup
// screens to answer. Built-in adapters cover common CLIs; TOML files in
// ~/.team-worker/agents add new CLIs or adjust built-in ones.
package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Session describes one agent start.
type Session struct {
	// Command is the agent command as given; Command[0] is the executable.
	Command []string
	Prompt  string
	// Interactive is true for the tmux backend, where the CLI keeps running
	// in a visible terminal; subprocess agents run their prompt and exit.
	Interactive     bool
	SkipPermissions bool
	// Team, AgentName and Leader identify the agent for adapters that install
	// team-worker hooks; WorkerBin is the team-worker binary those hooks run.
	Team      string
	AgentName string
	Leader    bool
	WorkerBin string
	// StateDir is where an adapter may ask spawn to write support files.
	StateDir string
}

// Plan is how to run one session.
type Plan struct {
	Args []string
	// Paste is typed into the tmux pane once the CLI is ready.
	Paste string
	// Stdin is fed to a subprocess agent.
	Stdin string
	Env   EnvPlan
	// Files are written before the agent starts, e.g. a pi extension.
	Files []File
}

// File is a support file an adapter needs on disk.
type File struct {
	Path    string
	Content []byte
}

// EnvPlan is an adapter's contribution to the agent environment.
type EnvPlan struct {
	// NoInherit starts from an empty environment holding only Pass.
	NoInherit bool
	Pass      []string
	Set       map[string]string
	Unset     []string
}

// Agent prepares sessions for one kind of agent CLI.
type Agent interface {
	Name() string
	// Source says where the adapter came from: "builtin" or a file path.
	Source() string
	Match(command []string) bool
	Prepare(s Session) Plan
	// StartupKeys returns the tmux keys that answer a startup screen, or nil.
	// screen is the pane text in lower case.
	StartupKeys(screen string) []string
	// Nudge reports whether the daemon may type message reminders into the
	// agent's terminal while it waits at its input prompt.
	Nudge() bool
}

// InputPromptVisible reports whether a terminal screen ends with an
// interactive CLI's input prompt (">", "❯" or "›", possibly inside a box),
// i.e. the CLI is waiting for the user rather than working or asking a
// question. Claude Code and Codex draw menu cursors with the same glyphs
// ("❯ 1. Yes"), so a numbered option anywhere near the bottom means a menu.
func InputPromptVisible(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	prompt := false
	for i := len(lines) - 1; i >= max(0, len(lines)-15); i-- {
		l := strings.TrimLeft(lines[i], " \t│┃|╭╰")
		cursor := strings.HasPrefix(l, "❯") || strings.HasPrefix(l, ">") || strings.HasPrefix(l, "›")
		rest := strings.TrimLeft(strings.TrimLeft(l, "❯>›"), " ")
		if menuOptionRE.MatchString(rest) {
			// A cursor on a numbered entry, or entries below the input line,
			// is a menu; a numbered list above the input line is only the
			// agent's last reply.
			if cursor || !prompt {
				return false
			}
			continue
		}
		if cursor {
			prompt = true
		}
	}
	return prompt
}

// menuOptionRE matches a numbered menu entry such as "1. Yes" or "2) No".
var menuOptionRE = regexp.MustCompile(`^\d+[.)]\s`)

// PromptMode is how a CLI takes its first prompt.
type PromptMode struct {
	Kind  string   // "arg", "flag", "paste" or "stdin"
	Flags []string // for "flag": the arguments before the prompt, e.g. ["--print", "-p"]
}

// ParsePromptMode reads "arg", "flag:<flags>", "paste" or "stdin";
// <flags> may hold several space-separated arguments.
func ParsePromptMode(s string) (PromptMode, error) {
	switch {
	case s == "arg" || s == "paste" || s == "stdin":
		return PromptMode{Kind: s}, nil
	case strings.HasPrefix(s, "flag:") && strings.TrimSpace(s[len("flag:"):]) != "":
		return PromptMode{Kind: "flag", Flags: strings.Fields(s[len("flag:"):])}, nil
	}
	return PromptMode{}, fmt.Errorf("invalid prompt mode %q: use arg, flag:<name>, paste or stdin", s)
}

// StartupRule presses Keys when the screen contains every string in All
// and at least one string from each group in Any.
type StartupRule struct {
	All  []string   `toml:"all"`
	Any  [][]string `toml:"any"`
	Keys []string   `toml:"keys"`
}

func (r StartupRule) matches(screen string) bool {
	if len(r.All) == 0 && len(r.Any) == 0 {
		return false
	}
	for _, s := range r.All {
		if !strings.Contains(screen, strings.ToLower(s)) {
			return false
		}
	}
	for _, group := range r.Any {
		if !slices.ContainsFunc(group, func(s string) bool { return strings.Contains(screen, strings.ToLower(s)) }) {
			return false
		}
	}
	return true
}

// cli is the adapter both built-in definitions and TOML files describe.
type cli struct {
	name     string
	source   string
	names    []string // executable basenames this adapter handles
	skipArgs []string
	skipEnv  map[string]string
	// tmuxPrompt and batchPrompt are the prompt modes for the tmux and subprocess backends.
	tmuxPrompt  PromptMode
	batchPrompt PromptMode
	env         EnvPlan
	startup     []StartupRule
	// adjust lets a built-in adapter rewrite the arguments, e.g. Codex's exec subcommand.
	adjust func(args []string, s Session) []string
	// hooks adds the arguments that install team-worker hooks; nil when the
	// CLI has none. hooksOn says whether they are installed by default; a TOML
	// adapter can switch them on or off.
	hooks   func(args []string, s Session, p *Plan) []string
	hooksOn bool
	// noNudge turns off terminal reminders for CLIs where typing into the
	// pane is unsafe.
	noNudge bool
}

func (c *cli) Name() string   { return c.name }
func (c *cli) Source() string { return c.source }

func (c *cli) Match(command []string) bool {
	return len(command) > 0 && slices.Contains(c.names, strings.ToLower(filepath.Base(command[0])))
}

func (c *cli) Prepare(s Session) Plan {
	args := slices.Clone(s.Command)
	env := EnvPlan{NoInherit: c.env.NoInherit, Pass: slices.Clone(c.env.Pass), Unset: slices.Clone(c.env.Unset),
		Set: map[string]string{}}
	for k, v := range c.env.Set {
		env.Set[k] = v
	}
	if s.SkipPermissions {
		args = append(args, c.skipArgs...)
		for k, v := range c.skipEnv {
			env.Set[k] = v
		}
	}
	if c.adjust != nil {
		args = c.adjust(args, s)
	}
	p := Plan{Env: env}
	if c.hooks != nil && c.hooksOn && s.WorkerBin != "" && s.Team != "" && s.AgentName != "" {
		args = c.hooks(args, s, &p)
	}
	mode := c.batchPrompt
	if s.Interactive {
		mode = c.tmuxPrompt
	}
	if s.Prompt != "" {
		switch mode.Kind {
		case "arg":
			args = append(args, s.Prompt)
		case "flag":
			args = append(append(args, mode.Flags...), s.Prompt)
		case "paste":
			p.Paste = s.Prompt
		case "stdin":
			p.Stdin = s.Prompt
		}
	}
	p.Args = args
	return p
}

func (c *cli) Nudge() bool { return !c.noNudge }

func (c *cli) StartupKeys(screen string) []string {
	for _, r := range c.startup {
		if r.matches(screen) {
			return r.Keys
		}
	}
	return nil
}

// clone copies an adapter so a TOML file can extend it without changing the original.
func (c *cli) clone() *cli {
	cp := *c
	cp.names = slices.Clone(c.names)
	cp.skipArgs = slices.Clone(c.skipArgs)
	cp.startup = slices.Clone(c.startup)
	cp.skipEnv = map[string]string{}
	for k, v := range c.skipEnv {
		cp.skipEnv[k] = v
	}
	cp.env = EnvPlan{NoInherit: c.env.NoInherit, Pass: slices.Clone(c.env.Pass),
		Unset: slices.Clone(c.env.Unset), Set: map[string]string{}}
	for k, v := range c.env.Set {
		cp.env.Set[k] = v
	}
	return &cp
}
