// Package templates loads TOML team templates for `team-worker launch`.
package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Agent struct {
	Name    string   `toml:"name" json:"name"`
	Type    string   `toml:"type" json:"type"`
	Task    string   `toml:"task" json:"task"`
	Command []string `toml:"command" json:"command,omitempty"`
	// Env applies to this agent on top of the template's env.
	Env map[string]string `toml:"env" json:"env,omitempty"`
}

// Task is created when the team launches. Key names the task so others can
// list it in BlockedBy; template tasks have no ids yet.
type Task struct {
	Key         string   `toml:"key" json:"key,omitempty"`
	Subject     string   `toml:"subject" json:"subject"`
	Description string   `toml:"description" json:"description"`
	Owner       string   `toml:"owner" json:"owner"`
	Priority    string   `toml:"priority" json:"priority,omitempty"`
	BlockedBy   []string `toml:"blocked_by" json:"blockedBy,omitempty"`
}

type Template struct {
	Name        string   `toml:"name" json:"name"`
	Description string   `toml:"description" json:"description"`
	Command     []string `toml:"command" json:"command"`
	Backend     string   `toml:"backend" json:"backend"`
	// Env applies to every agent; values may use ${NAME} from the caller's environment.
	Env    map[string]string `toml:"env" json:"env,omitempty"`
	Leader Agent             `toml:"leader" json:"leader"`
	Agents []Agent           `toml:"agents" json:"agents"`
	Tasks  []Task            `toml:"tasks" json:"tasks"`
	Source string            `toml:"-" json:"source"`
}

type file struct {
	Template Template `toml:"template"`
}

func parse(data []byte, fallbackName, source string) (Template, error) {
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return Template{}, err
	}
	t := f.Template
	if t.Name == "" {
		t.Name = fallbackName
	}
	if len(t.Command) == 0 {
		t.Command = []string{"claude"}
	}
	if t.Backend == "" {
		t.Backend = "tmux"
	}
	if t.Leader.Name == "" {
		return Template{}, fmt.Errorf("template %s: [template.leader] needs a name", t.Name)
	}
	for _, a := range append([]Agent{t.Leader}, t.Agents...) {
		if a.Name == "" {
			return Template{}, fmt.Errorf("template %s: every agent needs a name", t.Name)
		}
	}
	keys := map[string]bool{}
	for _, task := range t.Tasks {
		if task.Key != "" {
			keys[task.Key] = true
		}
	}
	for _, task := range t.Tasks {
		for _, dep := range task.BlockedBy {
			if !keys[dep] {
				return Template{}, fmt.Errorf("template %s: task %q is blocked by unknown key %q", t.Name, task.Subject, dep)
			}
		}
	}
	t.Source = source
	return t, nil
}

// Load reads <dir>/<name>.toml, or a template file given by path.
func Load(name, dir string) (Template, error) {
	path := filepath.Join(dir, name+".toml")
	if strings.HasSuffix(name, ".toml") {
		path = name
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Template{}, fmt.Errorf("template '%s' not found: create %s (see docs/templates.md)", name, path)
	}
	if err != nil {
		return Template{}, err
	}
	return parse(data, strings.TrimSuffix(filepath.Base(path), ".toml"), path)
}

// List parses every *.toml template in dir, sorted by name.
func List(userDir string) ([]Template, error) {
	found := map[string]Template{}
	paths, _ := filepath.Glob(filepath.Join(userDir, "*.toml"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		t, err := parse(data, strings.TrimSuffix(filepath.Base(p), ".toml"), p)
		if err != nil {
			return nil, err
		}
		found[t.Name] = t
	}
	list := make([]Template, 0, len(found))
	for _, t := range found {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// Render fills {goal}, {team_name}, {agent_name} and {leader_name}; other
// braces are left as written.
func Render(text, goal, team, agent, leader string) string {
	return strings.NewReplacer("{goal}", goal, "{team_name}", team, "{agent_name}", agent,
		"{leader_name}", leader).Replace(text)
}
