package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The launch file carries the environment and a command longer than tmux
// takes on its command line, runs both and removes itself.
func TestLaunchFileRunsLongCommand(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	long := strings.Repeat("x", 40000)
	command := "printf '%s %s' \"$TW_TEST_VAR\" " + shellQuote(long) + " > " + shellQuote(out)
	path, err := writeLaunchFile(filepath.Join(dir, "run"), map[string]string{"TW_TEST_VAR": "set", "PATH": os.Getenv("PATH")}, command)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := exec.Command("sh", "-c", ". "+shellQuote(path)).CombinedOutput(); err != nil {
		t.Fatalf("sourcing the launch file: %v: %s", err, res)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "set "+long {
		t.Fatalf("command output = %.40q…, want the variable and the long argument", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launch file still exists: %v", err)
	}
}
