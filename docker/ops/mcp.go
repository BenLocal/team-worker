package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// mcpCmd installs the named MCP server definitions in mcp.yaml through
// Magpie Library; servers it does not list are left alone.
func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp FILE",
		Short: "Install MCP server definitions from mcp.yaml through Magpie Library",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := configureMCP(magpieWebBase, args[0], cmd.OutOrStdout())
			return failWith("MCP setup failed: ", err)
		},
	}
}

var mcpFields = []string{"name", "transport", "command", "args", "env", "url", "headers", "agents", "install"}

func readMCPServers(path string) ([]map[string]any, error) {
	v, err := loadYAML(path)
	if err != nil {
		return nil, err
	}
	config, ok := asMap(v)
	if !ok || !keysExactly(config, "servers") {
		return nil, errors.New("mcp.yaml must contain only a servers list")
	}
	items, ok := config["servers"].([]any)
	if !ok {
		return nil, errors.New("servers must be a list")
	}
	names := map[string]bool{}
	servers := make([]map[string]any, 0, len(items))
	for _, item := range items {
		server, ok := asMap(item)
		if !ok || !keysWithin(server, mcpFields...) {
			return nil, errors.New("MCP server has unknown fields")
		}
		for _, field := range []string{"name", "transport"} {
			if s, ok := server[field].(string); !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("MCP %s must be a non-empty string", field)
			}
		}
		name := server["name"].(string)
		if names[name] {
			return nil, errors.New("MCP server names must be unique")
		}
		names[name] = true
		if v, ok := server["install"]; ok {
			install, ok := asMap(v)
			if !ok || !keysExactly(install, "command", "check") {
				return nil, errors.New("MCP install must have command and check argument lists")
			}
			for _, command := range install {
				if _, ok := commandArgs(command); !ok {
					return nil, errors.New("MCP install and check commands must be non-empty lists of string arguments")
				}
			}
		}
		for _, field := range []string{"agents", "args"} {
			v, present := server[field]
			if !present && field == "args" {
				continue
			}
			if _, ok := stringList(v); !ok {
				return nil, fmt.Errorf("MCP %s must be a list of strings", field)
			}
		}
		for _, field := range []string{"env", "headers"} {
			if v, ok := server[field]; ok {
				if _, ok := stringMap(v); !ok {
					return nil, fmt.Errorf("MCP %s must be a string map", field)
				}
			}
		}
		switch server["transport"] {
		case "stdio":
			command, _ := server["command"].(string)
			_, hasURL := server["url"]
			_, hasHeaders := server["headers"]
			if strings.TrimSpace(command) == "" || hasURL || hasHeaders {
				return nil, errors.New("stdio MCP needs command and cannot set url or headers")
			}
		case "http", "sse":
			raw, _ := server["url"].(string)
			u, err := url.Parse(raw)
			_, hasCommand := server["command"]
			_, hasArgs := server["args"]
			_, hasEnv := server["env"]
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
				hasCommand || hasArgs || hasEnv {
				return nil, errors.New("remote MCP needs an HTTP(S) url and cannot set command, args or env")
			}
		default:
			return nil, errors.New("MCP transport must be stdio, http or sse")
		}
		servers = append(servers, server)
	}
	return servers, nil
}

// commandArgs converts v to a command line whose executable is not blank.
func commandArgs(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	args := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		args = append(args, s)
	}
	if strings.TrimSpace(args[0]) == "" {
		return nil, false
	}
	return args, true
}

// runInstallCommand returns the command's exit status, 127 when it cannot be
// found. Its output is discarded because it may contain credentials.
func runInstallCommand(args []string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = downloadEnv()
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, errors.New("MCP dependency install or check timed out")
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return 127, nil
	}
	return 0, err
}

// installMCPServer runs a server's install command unless its check passes
// and the same install definition already succeeded.
func installMCPServer(server map[string]any, out io.Writer) error {
	install, ok := asMap(server["install"])
	if !ok {
		return nil
	}
	// json.Marshal sorts map keys, so the fingerprint is stable.
	raw, err := json.Marshal(install)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	nameSum := sha256.Sum256([]byte(server["name"].(string)))
	stateFile := filepath.Join(configHome(), "team-worker-docker/mcp-installs", hex.EncodeToString(nameSum[:])+".json")
	saved, err := readJSONFile(stateFile)
	if err != nil {
		return err
	}
	command, _ := commandArgs(install["command"])
	check, _ := commandArgs(install["check"])
	status, err := runInstallCommand(check, 60*time.Second)
	if err != nil {
		return err
	}
	if status == 0 && saved["fingerprint"] == fingerprint {
		return nil
	}
	status, err = runInstallCommand(command, 600*time.Second)
	if err != nil {
		return err
	}
	if status == 0 {
		if status, err = runInstallCommand(check, 60*time.Second); err != nil {
			return err
		}
	}
	if status != 0 {
		return errors.New("MCP dependency install failed or the check after install did not pass")
	}
	state, _ := json.Marshal(map[string]string{"fingerprint": fingerprint})
	if err := writeAtomic(stateFile, state, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(out, "mcp: dependencies installed and checked.")
	return nil
}

func configureMCP(base, path string, out io.Writer) error {
	servers, err := readMCPServers(path)
	if err != nil || len(servers) == 0 {
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
	known := agentIDs(view["agents"], "id")
	for _, server := range servers {
		agents, _ := stringList(server["agents"])
		for _, agent := range agents {
			if !known[agent] {
				return errors.New("mcp.yaml names agents that Magpie does not know")
			}
		}
	}
	names := agentIDs(view["servers"], "name")
	for i, server := range servers {
		if err := installMCPServer(server, out); err != nil {
			return err
		}
		name := server["name"].(string)
		old := ""
		if names[name] {
			old = name
		}
		definition := map[string]any{}
		for k, v := range server {
			if k != "install" {
				definition[k] = v
			}
		}
		if _, err := lib.Change("servers/save", map[string]any{"old": old, "server": definition}); err != nil {
			return err
		}
		names[name] = true
		fmt.Fprintf(out, "mcp: configured server %d.\n", i+1)
	}
	return nil
}
