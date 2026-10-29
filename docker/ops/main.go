// Command team-worker-ops prepares and supervises the services inside the
// team-worker Docker image: Magpie configuration, skills, MCP servers,
// plugins, instructions, OpenBao secrets, Docker proxies, systemd hand-off
// and the leader runtime.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:               "team-worker-ops",
		Short:             "Bootstrap and runtime helpers for the team-worker Docker image",
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.AddCommand(
		proxyCmd(), magpieCmd(), pluginsCmd(), skillsCmd(), mcpCmd(), promptCmd(),
		openbaoCmd(), systemdCmd(), leaderCmd(), maskingCmd(),
	)
	if err := root.Execute(); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			if exit.msg != "" {
				fmt.Fprintln(os.Stderr, exit.msg)
			}
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// exitError carries the exact message and status a subcommand exits with.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// failWith formats err behind a user-facing prefix, e.g. "plugin setup failed: ".
// Callers pass errors whose text is safe to print: no keys or upstream bodies.
func failWith(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return exitError{code: 1, msg: prefix + err.Error()}
}
