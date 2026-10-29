package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/benlocal/team-worker/internal/agent"
)

// EnvLayer is one named source of environment variables. Values may use
// ${NAME} to take a variable from the caller's environment.
type EnvLayer struct {
	Name  string
	Set   map[string]string
	Unset []string
}

type EnvSpec struct {
	// Caller is the environment team-worker runs in, as from os.Environ.
	Caller []string
	// Adapter is the agent adapter's environment; AdapterName labels it.
	Adapter     agent.EnvPlan
	AdapterName string
	// Layers apply in order after the adapter: template, template agent, --env.
	Layers []EnvLayer
	// Identity holds the TEAM_WORKER_* variables; no layer may override them.
	Identity map[string]string
	// WorkerBin's directory goes first on PATH so agents find team-worker.
	WorkerBin string
}

// Env is the composed environment and, for each variable, the layer that set it.
type Env struct {
	Vars   map[string]string
	Origin map[string]string
}

var refRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ComposeEnv builds an agent's environment from the caller's environment,
// the adapter, the configured layers and the team-worker identity, in that order.
func ComposeEnv(spec EnvSpec) (Env, error) {
	caller := map[string]string{}
	for _, kv := range spec.Caller {
		if k, v, ok := strings.Cut(kv, "="); ok {
			caller[k] = v
		}
	}
	e := Env{Vars: map[string]string{}, Origin: map[string]string{}}
	set := func(k, v, origin string) { e.Vars[k], e.Origin[k] = v, origin }
	unset := func(k string) { delete(e.Vars, k); delete(e.Origin, k) }

	for k, v := range caller {
		if !spec.Adapter.NoInherit || slices.Contains(spec.Adapter.Pass, k) {
			set(k, v, "caller")
		}
	}
	// Interactive CLIs such as Codex refuse TERM=dumb, which non-interactive shells pass down.
	if t := e.Vars["TERM"]; t == "" || strings.EqualFold(t, "dumb") {
		set("TERM", "xterm-256color", "team-worker")
	}
	// The agent gets its own tmux pane; it must not think it runs inside the caller's.
	unset("TMUX")
	unset("TMUX_PANE")

	layers := append([]EnvLayer{{Name: "adapter " + spec.AdapterName, Set: spec.Adapter.Set,
		Unset: spec.Adapter.Unset}}, spec.Layers...)
	for _, l := range layers {
		for _, k := range l.Unset {
			unset(k)
		}
		for k, v := range l.Set {
			if _, ok := spec.Identity[k]; ok {
				return Env{}, fmt.Errorf("%s sets %s, which team-worker manages", l.Name, k)
			}
			expanded, err := expand(v, caller)
			if err != nil {
				return Env{}, fmt.Errorf("%s: %s: %w", l.Name, k, err)
			}
			set(k, expanded, l.Name)
		}
	}
	for k, v := range spec.Identity {
		set(k, v, "team-worker")
	}
	if spec.WorkerBin != "" {
		path := filepath.Dir(spec.WorkerBin)
		if p := e.Vars["PATH"]; p != "" {
			path += string(os.PathListSeparator) + p
		}
		set("PATH", path, e.Origin["PATH"]+" + team-worker")
	}
	return e, nil
}

// expand replaces ${NAME} with the caller's variable; a missing one is an
// error, so a typo never becomes an empty credential.
func expand(v string, caller map[string]string) (string, error) {
	var missing string
	out := refRE.ReplaceAllStringFunc(v, func(ref string) string {
		name := refRE.FindStringSubmatch(ref)[1]
		val, ok := caller[name]
		if !ok && missing == "" {
			missing = name
		}
		return val
	})
	if missing != "" {
		return "", fmt.Errorf("${%s} is not set in the environment", missing)
	}
	return out, nil
}
