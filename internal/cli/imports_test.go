package cli

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// agentRuntime are the CLI files that run inside an agent's pane, on the
// daemon's machine: the keepalive supervisor and the CLI hooks. Only they may
// touch processes and panes themselves.
var agentRuntime = map[string]bool{"supervise.go": true, "hook.go": true}

// TestClientCommandsLeaveProcessesToTheDaemon keeps the CLI a client: what
// acts on processes, panes and the agents' files belongs in team-workerd
// (server and runner), so a command behaves the same over the socket and
// over HTTP.
func TestClientCommandsLeaveProcessesToTheDaemon(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
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
			switch pkg {
			case "github.com/benlocal/team-worker/internal/spawn", "github.com/benlocal/team-worker/internal/proc",
				"github.com/benlocal/team-worker/internal/tmux":
				t.Errorf("%s imports %s: the CLI is a client; do the process or pane work in team-workerd "+
					"(internal/server or internal/runner) behind an API call", path, pkg)
			}
		}
	}
}
