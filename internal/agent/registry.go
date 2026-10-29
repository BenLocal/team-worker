package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Registry picks the adapter for a command: user adapters first, then
// built-ins, then the generic fallback.
type Registry struct {
	agents []Agent
}

// Builtin returns a registry with only the built-in adapters.
func Builtin() *Registry {
	r := &Registry{}
	for _, c := range builtins() {
		c.source = "builtin"
		r.agents = append(r.agents, c)
	}
	return r
}

// Load reads every *.toml adapter in dir on top of the built-ins. A missing
// directory is not an error.
func Load(dir string) (*Registry, error) {
	base := Builtin()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	sort.Strings(paths)
	var user []Agent
	for _, p := range paths {
		a, err := loadFile(p)
		if err != nil {
			return nil, err
		}
		user = append(user, a)
	}
	return &Registry{agents: append(user, base.agents...)}, nil
}

// For returns the first adapter that matches the command.
func (r *Registry) For(command []string) Agent {
	for _, a := range r.agents {
		if a.Match(command) {
			return a
		}
	}
	return generic()
}

// file is the TOML form of an adapter.
type file struct {
	Name               string            `toml:"name"`
	Extends            string            `toml:"extends"`
	Match              []string          `toml:"match"`
	SkipPermissions    []string          `toml:"skip_permissions"`
	SkipPermissionsEnv map[string]string `toml:"skip_permissions_env"`
	Prompt             string            `toml:"prompt"`
	PromptTmux         string            `toml:"prompt_tmux"`
	PromptSubprocess   string            `toml:"prompt_subprocess"`
	Inherit            *bool             `toml:"inherit"`
	Pass               []string          `toml:"pass"`
	Unset              []string          `toml:"unset"`
	Env                map[string]string `toml:"env"`
	Startup            []StartupRule     `toml:"startup"`
	// Hooks switches a built-in adapter's team-worker hooks on or off.
	Hooks *bool `toml:"hooks"`
	// Nudge set to false stops the daemon from typing reminders into the pane.
	Nudge *bool `toml:"nudge"`
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func loadFile(path string) (Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("agent adapter %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("agent adapter %s: unknown key %s", path, undecoded[0])
	}
	c, err := f.build(path)
	if err != nil {
		return nil, fmt.Errorf("agent adapter %s: %w", path, err)
	}
	return c, nil
}

func (f file) build(path string) (*cli, error) {
	var c *cli
	if f.Extends != "" {
		for _, b := range builtins() {
			if b.name == f.Extends {
				c = b.clone()
			}
		}
		if c == nil {
			return nil, fmt.Errorf("extends unknown built-in adapter %q", f.Extends)
		}
	} else {
		c = generic()
	}
	c.source = path
	c.name = f.Name
	if c.name == "" {
		c.name = strings.TrimSuffix(filepath.Base(path), ".toml")
	}
	if f.Match != nil {
		c.names = nil
		for _, m := range f.Match {
			c.names = append(c.names, strings.ToLower(m))
		}
	}
	if len(c.names) == 0 {
		return nil, fmt.Errorf("set match to the executable names this adapter handles")
	}
	if f.SkipPermissions != nil {
		c.skipArgs = f.SkipPermissions
	}
	if f.SkipPermissionsEnv != nil {
		c.skipEnv = f.SkipPermissionsEnv
	}
	for _, m := range []struct {
		value  string
		target []*PromptMode
	}{
		{f.Prompt, []*PromptMode{&c.tmuxPrompt, &c.batchPrompt}},
		{f.PromptTmux, []*PromptMode{&c.tmuxPrompt}},
		{f.PromptSubprocess, []*PromptMode{&c.batchPrompt}},
	} {
		if m.value == "" {
			continue
		}
		mode, err := ParsePromptMode(m.value)
		if err != nil {
			return nil, err
		}
		for _, t := range m.target {
			*t = mode
		}
	}
	if f.Hooks != nil {
		if *f.Hooks && c.hooks == nil {
			return nil, fmt.Errorf("hooks = true needs extends with a built-in adapter that has hooks (claude, codex or pi)")
		}
		c.hooksOn = *f.Hooks
	}
	if f.Nudge != nil {
		c.noNudge = !*f.Nudge
	}
	if f.Inherit != nil {
		c.env.NoInherit = !*f.Inherit
	}
	c.env.Pass = append(c.env.Pass, f.Pass...)
	c.env.Unset = append(c.env.Unset, f.Unset...)
	if c.env.Set == nil {
		c.env.Set = map[string]string{}
	}
	for k, v := range f.Env {
		c.env.Set[k] = v
	}
	for _, names := range [][]string{c.env.Pass, c.env.Unset, keys(c.env.Set), keys(c.skipEnv)} {
		for _, k := range names {
			if !envNameRE.MatchString(k) {
				return nil, fmt.Errorf("invalid environment variable name %q", k)
			}
		}
	}
	for _, r := range f.Startup {
		if len(r.Keys) == 0 || (len(r.All) == 0 && len(r.Any) == 0) {
			return nil, fmt.Errorf("every [[startup]] rule needs keys and at least one of all or any")
		}
	}
	// User rules are checked before the inherited ones.
	c.startup = append(f.Startup, c.startup...)
	return c, nil
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
