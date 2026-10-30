package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

// fakeTmux puts a tmux on PATH whose only pane is %0, started as pid 200 and
// alive; it records every kill-pane in the returned file.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	kills := filepath.Join(dir, "kills")
	script := `#!/bin/sh
case "$1" in
display-message) [ "$4" = "%0" ] && echo "%0 0 200" && exit 0; echo "can't find pane: $4" >&2; exit 1 ;;
kill-pane) echo "$3" >> "` + kills + `"; exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return kills
}

func TestTmuxPaneOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     model.Agent
		wantAlive bool
		wantKill  bool
	}{
		{"own pane", model.Agent{Backend: "tmux", Target: "%0", PID: 200}, true, true},
		{"pid not recorded", model.Agent{Backend: "tmux", Target: "%0"}, true, true},
		// After a tmux server restart %0 belongs to whoever was started first.
		{"reused pane id", model.Agent{Backend: "tmux", Target: "%0", PID: 100}, false, false},
		{"pane gone", model.Agent{Backend: "tmux", Target: "%7", PID: 200}, false, false},
		{"no pane", model.Agent{Backend: "tmux"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kills := fakeTmux(t)
			if alive, _ := Probe(tc.agent); alive != tc.wantAlive {
				t.Fatalf("Probe alive = %v, want %v", alive, tc.wantAlive)
			}
			if err := Stop(tc.agent); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			data, _ := os.ReadFile(kills)
			if killed := strings.TrimSpace(string(data)) != ""; killed != tc.wantKill {
				t.Fatalf("kill-pane called = %v (%q), want %v", killed, data, tc.wantKill)
			}
			if _, err := Screen(tc.agent, 0); tc.wantAlive == (err != nil && strings.Contains(err.Error(), "closed")) {
				t.Fatalf("Screen error = %v with alive %v", err, tc.wantAlive)
			}
		})
	}
}
