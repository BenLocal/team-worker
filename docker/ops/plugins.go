package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// pluginInstallTimeout bounds one `magpie plugin add`; tests shorten it.
var pluginInstallTimeout = 360 * time.Second

// pluginsCmd preinstalls the native Magpie plugins in plugin.yaml. A plugin
// that fails to install is skipped and retried on the next start.
func pluginsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plugins FILE",
		Short: "Install the Magpie plugins listed in plugin.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := configurePlugins(args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
			return failWith("plugin setup failed: ", err)
		},
	}
}

func readPlugins(path string) ([]string, error) {
	v, err := loadYAML(path)
	if err != nil {
		return nil, err
	}
	config, ok := asMap(v)
	if !ok || !keysExactly(config, "plugins") {
		return nil, errors.New("plugin.yaml must contain only a plugins list")
	}
	plugins, ok := stringList(config["plugins"])
	if ok {
		for _, spec := range plugins {
			if strings.HasPrefix(spec, "-") {
				ok = false
			}
		}
	}
	if !ok {
		return nil, errors.New("plugins must be a list of non-empty plugin source strings")
	}
	seen := map[string]bool{}
	for _, spec := range plugins {
		if seen[spec] {
			return nil, errors.New("plugin sources in plugin.yaml must be unique")
		}
		seen[spec] = true
	}
	return plugins, nil
}

// installedPluginSpecs reads the specs Magpie has recorded as installed.
func installedPluginSpecs(profile string) (map[string]bool, error) {
	data, err := readJSONFile(profile)
	if err != nil {
		return nil, err
	}
	return agentIDs(data["plugins"], "spec"), nil
}

// configurePlugins installs the listed plugins and returns the 1-based
// indexes of those that failed to install.
func configurePlugins(path string, out, errOut io.Writer) ([]int, error) {
	plugins, err := readPlugins(path)
	if err != nil || len(plugins) == 0 {
		return nil, err
	}
	profile := filepath.Join(configHome(), "magpie/plugins.json")
	stateFile := filepath.Join(configHome(), "team-worker-docker/plugin-sources.json")
	saved, err := readJSONFile(stateFile)
	if err != nil {
		return nil, err
	}
	state := map[string]string{}
	for k, v := range saved {
		if s, ok := v.(string); ok {
			state[k] = s
		}
	}
	var failed []int
	for i, source := range plugins {
		index := i + 1
		specs, err := installedPluginSpecs(profile)
		if err != nil {
			return failed, err
		}
		canonical := strings.TrimPrefix(source, "npm:")
		if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
			canonical = resolvePath(source)
		}
		if spec, ok := state[source]; ok && specs[spec] {
			fmt.Fprintf(out, "plugin: plugin %d is already installed; keeping its configuration.\n", index)
			continue
		}
		stdout, ok := addPlugin(source)
		if !ok {
			// A plugin is optional for the team: skip it, keep it unrecorded so
			// the next start retries, and never echo output that may hold credentials.
			fmt.Fprintf(errOut, "plugin: plugin %d failed to install and was skipped; it will be retried on the next start. "+
				"Check the source, Git credentials and network.\n", index)
			failed = append(failed, index)
			continue
		}
		// Magpie resolves community aliases to the spec printed by its add command.
		resolved := canonical
		scanner := bufio.NewScanner(bytes.NewReader(stdout))
		for scanner.Scan() {
			if spec, found := strings.CutPrefix(scanner.Text(), "✓ added "); found {
				resolved = strings.TrimSpace(spec)
				break
			}
		}
		if resolved != "" {
			state[source] = resolved
			data, _ := json.Marshal(state)
			if err := writeAtomic(stateFile, data, 0o600); err != nil {
				return failed, err
			}
		}
		fmt.Fprintf(out, "plugin: installed plugin %d.\n", index)
	}
	return failed, nil
}

// resolvePath makes a local plugin path absolute, following symlinks when
// the path exists.
func resolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// addPlugin runs `magpie plugin add` and reports whether it succeeded.
func addPlugin(source string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginInstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "magpie", "plugin", "add", source)
	cmd.Env = downloadEnv()
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		return nil, false
	}
	return stdout.Bytes(), true
}
