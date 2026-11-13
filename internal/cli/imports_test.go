package cli

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// agentRuntime are the CLI files that run inside an agent's pane, on the
// daemon's machine: the keepalive supervisor and the CLI hooks. Only they may
// touch processes and panes themselves.
var agentRuntime = map[string]bool{"supervise.go": true, "hook.go": true}

// daemonOnly are the packages that act on processes, panes and the agents'
// machine; they belong to team-workerd.
var daemonOnly = []string{
	"github.com/benlocal/team-worker/internal/spawn", "github.com/benlocal/team-worker/internal/proc",
	"github.com/benlocal/team-worker/internal/tmux", "github.com/benlocal/team-worker/internal/runner",
	"github.com/benlocal/team-worker/internal/memory", "github.com/benlocal/team-worker/internal/board/live", "github.com/benlocal/team-worker/internal/workspace",
}

// TestClientCommandsLeaveProcessesToTheDaemon keeps the CLI a client: what
// acts on processes, panes and the agents' files belongs in team-workerd, so
// a command behaves the same over the socket and over HTTP. It follows the
// packages the client commands import all the way down, since a helper
// package can drag process code in as well.
func TestClientCommandsLeaveProcessesToTheDaemon(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	importedBy := map[string][]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || agentRuntime[path] {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			pkg, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(pkg, "github.com/benlocal/team-worker/") {
				importedBy[pkg] = append(importedBy[pkg], path)
			}
		}
	}
	for pkg, by := range importedBy {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if slices.Contains(daemonOnly, dep) {
				t.Errorf("%s import %s, which brings in %s: the CLI is a client; do the process or pane work "+
					"in team-workerd (internal/server or internal/runner) behind an API call", strings.Join(by, ", "), pkg, dep)
			}
		}
	}
}
