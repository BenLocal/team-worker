package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// stateDir passes runtime configuration from the entrypoint to systemd units.
var stateDir = "/run/team-worker"

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// errSystemd hides details that may include environment values.
var errSystemd = exitError{code: 1, msg: "systemd setup failed; check /run/team-worker and the Docker service."}

func systemdCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "systemd",
		Short: "Pass runtime configuration to systemd without shell-parsing commands or keys",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "prepare [ARGS...]",
			Short: "Write the environment files and the agent command for systemd units",
			Args:  cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := systemdPrepare(args); err != nil {
					return errSystemd
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "agent",
			Short: "Wait for Docker and Magpie, then exec the container entrypoint",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return systemdAgent() },
		},
		&cobra.Command{
			Use:   "magpie",
			Short: "Exec the Magpie gateway and Web UI with a persistent sign-in key",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if err := systemdMagpie(); err != nil {
					return errSystemd
				}
				return nil
			},
		},
	)
	return cmd
}

// writeEnvironment writes a systemd EnvironmentFile. Values are double-quoted,
// which supports multiline values; $ is escaped so it stays literal. Names
// systemd cannot express are skipped.
func writeEnvironment(path string, env map[string]string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	var b strings.Builder
	for _, name := range names {
		if environmentName.MatchString(name) {
			b.WriteString(name + `="` + escaper.Replace(env[name]) + "\"\n")
		}
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func systemdPrepare(args []string) error {
	node, err := user.Lookup("node")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(node.Gid)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return err
	}
	if err := os.Chown(stateDir, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0o750); err != nil {
		return err
	}
	env := environMap()
	env["HOME"], env["USER"], env["LOGNAME"] = node.HomeDir, node.Username, node.Username
	if err := writeEnvironment(filepath.Join(stateDir, "agent.env"), env); err != nil {
		return err
	}
	if err := writeEnvironment(filepath.Join(stateDir, "docker.env"), dockerEnvironment(env)); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"start"}
	}
	data, err := json.Marshal(map[string]any{"cwd": cwd, "args": args})
	if err != nil {
		return err
	}
	command := filepath.Join(stateDir, "command.json")
	if err := os.WriteFile(command, data, 0o640); err != nil {
		return err
	}
	if err := os.Chown(command, 0, gid); err != nil {
		return err
	}
	return os.Chmod(command, 0o640)
}

func systemdAgent() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ID}}").Run()
	cancel()
	if err != nil {
		return exitError{code: 1, msg: "Docker service is not ready; check systemctl status docker."}
	}
	ready := false
	for range 150 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "magpie", "healthcheck").Run()
		cancel()
		if err == nil {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return exitError{code: 1, msg: "Magpie service is not ready; check systemctl status magpie."}
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "command.json"))
	if err != nil {
		return errSystemd
	}
	var command struct {
		Cwd  string   `json:"cwd"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(data, &command); err != nil {
		return errSystemd
	}
	if err := os.Chdir(command.Cwd); err != nil {
		return errSystemd
	}
	if err := execProgram("container-entrypoint", command.Args...); err != nil {
		return errSystemd
	}
	return nil
}

// ensureWebKey returns the Magpie Web key, creating it on first use so the
// browser sign-in link stays stable on the same home volume.
func ensureWebKey() (string, error) {
	path := filepath.Join(homeDir(), ".config/magpie/web.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		data = []byte(hex.EncodeToString(raw) + "\n")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return "", err
		}
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(data)), nil
}

func systemdMagpie() error {
	key, err := ensureWebKey()
	if err != nil {
		return err
	}
	base := os.Getenv("TEAM_WORKER_WEB_URL")
	if base == "" {
		port := os.Getenv("TEAM_WORKER_WEB_PORT")
		if port == "" {
			port = "8080"
		}
		base = "http://localhost:" + port
	}
	os.Setenv("MAGPIE_WEB_KEY", key)
	os.Setenv("MAGPIE_WEB_URL", strings.TrimRight(base, "/")+"/magpie")
	return execProgram("magpie", "web", "--addr", "127.0.0.1:3430", "--no-open")
}

// execProgram replaces the current process with name, found on PATH.
func execProgram(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{name}, args...), os.Environ())
}
