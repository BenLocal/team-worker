// Package cli implements the team-worker command line client.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
)

type app struct {
	jsonOut bool
	out     io.Writer
	errOut  io.Writer
	in      io.Reader
	// newClient is replaced in tests.
	newClient func() *client.Client
	c         *client.Client
}

func (a *app) client() *client.Client {
	if a.c == nil {
		a.c = a.newClient()
	}
	return a.c
}

// exitError carries a message that has already been reported to the user.
type exitError struct{}

func (exitError) Error() string { return "exit 1" }

// NewRoot builds the command tree. A nil newClient connects to the daemon
// for the configured data directory.
func NewRoot(newClient func() *client.Client) *cobra.Command {
	a := &app{out: os.Stdout, errOut: os.Stderr, in: os.Stdin, newClient: newClient}
	if a.newClient == nil {
		a.newClient = func() *client.Client { return client.New(config.DataDir()) }
	}
	root := &cobra.Command{
		Use:           "team-worker",
		Short:         "Coordinate a team of CLI agents through team-workerd",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			a.out, a.errOut, a.in = cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin()
		},
	}
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "Output as JSON")
	root.AddCommand(a.teamCmd(), a.taskCmd(), a.inboxCmd(), a.identityCmd(), a.spawnCmd(), a.lifecycleCmd(),
		a.agentCmd(), a.boardCmd(), a.templateCmd(), a.launchCmd(), a.hookCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := NewRoot(nil)
	err := root.Execute()
	if err == nil {
		return 0
	}
	if !errors.As(err, new(exitError)) {
		jsonOut, _ := root.PersistentFlags().GetBool("json")
		if jsonOut {
			printJSON(os.Stdout, map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
	}
	return 1
}

// emit prints data as JSON with --json, otherwise calls human.
func (a *app) emit(data any, human func(w io.Writer)) {
	if a.jsonOut || human == nil {
		printJSON(a.out, data)
		return
	}
	human(a.out)
}

// fail reports an error in the active output mode and returns exitError.
func (a *app) fail(err error) error {
	if a.jsonOut {
		printJSON(a.out, map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(a.errOut, err)
	}
	return exitError{}
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// printJSONLine writes one compact JSON object per line for streaming output.
func printJSONLine(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func newTable(w io.Writer, headers ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	return tw
}

func row(tw *tabwriter.Writer, cols ...string) {
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
}

func formatTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (a *app) confirm(prompt string) bool {
	fmt.Fprintf(a.errOut, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
