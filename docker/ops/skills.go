package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// skillsCmd imports the skill sources in skills.yaml through Magpie Library
// before the leader starts; --update refreshes Git skills hourly.
func skillsCmd() *cobra.Command {
	var update bool
	cmd := &cobra.Command{
		Use:   "skills [--update] [FILE]",
		Short: "Install skill sources from skills.yaml through Magpie Library",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := os.Getenv("TEAM_WORKER_SKILLS_FILE")
			if path == "" {
				path = "/etc/team-worker/skills.yaml"
			}
			if len(args) == 1 {
				path = args[0]
			}
			err := configureSkills(magpieWebBase, path, update, cmd.OutOrStdout())
			return failWith("skills setup failed: ", err)
		},
	}
	cmd.Flags().BoolVar(&update, "update", false, "Update configured Git skills while preserving local edits")
	return cmd
}

type skillSource struct {
	Source string
	Agents []string
}

func readSkillSources(path string) ([]skillSource, error) {
	v, err := loadYAML(path)
	if err != nil {
		return nil, err
	}
	config, ok := asMap(v)
	if !ok || !keysExactly(config, "sources") {
		return nil, errors.New("skills.yaml must contain only a sources list")
	}
	items, ok := config["sources"].([]any)
	if !ok {
		return nil, errors.New("sources in skills.yaml must be a list")
	}
	sources := make([]skillSource, 0, len(items))
	for i, item := range items {
		label := fmt.Sprintf("skills.yaml source %d", i+1)
		m, ok := asMap(item)
		if !ok || !keysExactly(m, "source", "agents") {
			return nil, fmt.Errorf("%s must have source and agents", label)
		}
		source, ok := m["source"].(string)
		if !ok || strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("%s: source must be a non-empty string", label)
		}
		agents, ok := stringList(m["agents"])
		if !ok || len(agents) == 0 {
			return nil, fmt.Errorf("%s: agents must be a non-empty list of strings", label)
		}
		sources = append(sources, skillSource{Source: source, Agents: agents})
	}
	return sources, nil
}

// repository reduces a source to a comparable repository identity, without
// credentials, refs or subfolders.
func repository(source string) string {
	for _, marker := range []string{"/-/tree/", "/-/blob/", "/src/branch/", "/src/tag/", "/src/commit/", "/tree/"} {
		source, _, _ = strings.Cut(source, marker)
	}
	if _, rest, ok := strings.Cut(source, "://"); ok {
		if i := strings.IndexAny(rest, "?#"); i >= 0 {
			rest = rest[:i]
		}
		netloc, path := rest, ""
		if i := strings.Index(rest, "/"); i >= 0 {
			netloc, path = rest[:i], rest[i:]
		}
		if i := strings.LastIndex(netloc, "@"); i >= 0 {
			netloc = netloc[i+1:]
		}
		source = strings.TrimPrefix(netloc+path, "github.com/")
	} else if strings.Contains(source, "@") && strings.Contains(source, ":") {
		_, after, _ := strings.Cut(source, "@")
		source = strings.Replace(after, ":", "/", 1)
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimRight(source, "/"), ".git"))
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[1:])
	}
	return path
}

func configureSkills(base, path string, update bool, out io.Writer) error {
	sources, err := readSkillSources(path)
	if err != nil || len(sources) == 0 {
		return err
	}
	key, err := magpieWebKey()
	if err != nil {
		return err
	}
	lib, err := newLibrary(base, key)
	if err != nil {
		return err
	}
	view, err := lib.View("/api/library", nil)
	if err != nil {
		return err
	}
	if _, ok := view["agents"].([]any); !ok {
		return errors.New("Magpie Library returned no agent list")
	}
	known := agentIDs(view["agents"], "id")
	for i, item := range sources {
		for _, agent := range item.Agents {
			if !known[agent] {
				return fmt.Errorf("skills.yaml source %d names an agent that Magpie does not know", i+1)
			}
		}
	}
	for i, item := range sources {
		index := i + 1
		if update && filepath.IsAbs(expandUser(item.Source)) {
			continue
		}
		probe, err := lib.View("/api/library/skills/probe", map[string]any{"source": item.Source})
		if err != nil {
			return err
		}
		candidates, _ := probe["candidates"].([]any)
		if len(candidates) == 0 {
			return fmt.Errorf("skills.yaml source %d has no skills", index)
		}
		var paths []string
		wanted := map[string]bool{}
		for _, c := range candidates {
			candidate, ok := asMap(c)
			if !ok {
				return errors.New("Magpie Library returned an invalid scan result")
			}
			skillPath, okPath := candidate["path"].(string)
			have, okHave := candidate["have"].(bool)
			if !okPath || !okHave {
				return errors.New("Magpie Library returned an invalid scan result")
			}
			name, _ := candidate["name"].(string)
			wanted[name] = true
			if have && !update {
				fmt.Fprintf(out, "skills: source %d already has %s; keeping its configuration.\n", index, name)
			} else if !have {
				paths = append(paths, skillPath)
			}
		}
		if len(paths) > 0 {
			result, err := lib.Change("skills/install", map[string]any{
				"source": item.Source, "paths": paths, "agents": item.Agents,
			})
			if err != nil {
				return err
			}
			installed, _ := result["installed"].([]any)
			fmt.Fprintf(out, "skills: source %d installed %d skills.\n", index, len(installed))
		}
		kind, _ := probe["kind"].(string)
		if update && (kind == "git" || kind == "github") {
			current, err := lib.View("/api/library", nil)
			if err != nil {
				return err
			}
			skills, _ := current["skills"].([]any)
			var names []string
			for _, s := range skills {
				skill, ok := asMap(s)
				if !ok {
					continue
				}
				name, _ := skill["name"].(string)
				skillKind, _ := skill["kind"].(string)
				origin, _ := skill["source"].(string)
				if origin == "" {
					origin, _ = skill["repo"].(string)
				}
				if wanted[name] && (skillKind == "git" || skillKind == "github") &&
					repository(origin) == repository(item.Source) {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				result, err := lib.Change("skills/update-some", map[string]any{"names": names})
				if err != nil {
					return err
				}
				updated, _ := result["updated"].([]any)
				fmt.Fprintf(out, "skills: source %d updated %d skills.\n", index, len(updated))
			}
		}
	}
	_, err = lib.Change("all/sync", map[string]any{})
	return err
}
