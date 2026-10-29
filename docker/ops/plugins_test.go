package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakePluginMagpie handles `magpie plugin add <source>`: "bad" fails with a
// credential in its output, "slow" hangs, others succeed.
const fakePluginMagpie = `#!/bin/sh
echo "$3" >> "$MAGPIE_CALLS"
if [ "$3" = "bad" ]; then
    echo "fetch https://user:SECRET@example.invalid/repo failed" >&2
    exit 1
fi
if [ "$3" = "slow" ]; then
    sleep 5
fi
echo "✓ added $3@1.0.0"
`

type pluginRun struct {
	failed []int
	state  map[string]string
	stderr string
	calls  string
	err    error
}

func runPlugins(t *testing.T, config string) pluginRun {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.Mkdir(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "magpie"), []byte(fakePluginMagpie), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("MAGPIE_CALLS", calls)
	path := writeFile(t, root, "plugin.yaml", config)
	var stderr bytes.Buffer
	failed, err := configurePlugins(path, io.Discard, &stderr)
	run := pluginRun{failed: failed, stderr: stderr.String(), err: err, state: map[string]string{}}
	if data, err := os.ReadFile(filepath.Join(root, "config/team-worker-docker/plugin-sources.json")); err == nil {
		json.Unmarshal(data, &run.state)
	}
	data, _ := os.ReadFile(calls)
	run.calls = string(data)
	return run
}

func TestPluginsFailedInstallIsSkippedAndNotRecorded(t *testing.T) {
	run := runPlugins(t, `{"plugins": ["bad", "good"]}`)
	if run.err != nil {
		t.Fatal(run.err)
	}
	if !reflect.DeepEqual(run.failed, []int{1}) {
		t.Errorf("failed = %v, want [1]", run.failed)
	}
	if !reflect.DeepEqual(run.state, map[string]string{"good": "good@1.0.0"}) {
		t.Errorf("state = %v", run.state)
	}
	if !strings.Contains(run.stderr, "plugin 1 failed to install") {
		t.Errorf("stderr = %q", run.stderr)
	}
	if strings.Contains(run.stderr, "SECRET") {
		t.Errorf("stderr leaks command output: %q", run.stderr)
	}
}

func TestPluginsTimeoutIsTreatedAsFailedInstall(t *testing.T) {
	old := pluginInstallTimeout
	pluginInstallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { pluginInstallTimeout = old })
	run := runPlugins(t, `{"plugins": ["slow"]}`)
	if run.err != nil {
		t.Fatal(run.err)
	}
	if !reflect.DeepEqual(run.failed, []int{1}) || len(run.state) != 0 {
		t.Errorf("failed = %v, state = %v", run.failed, run.state)
	}
}

func TestPluginsInvalidConfigFailsBeforeInstalling(t *testing.T) {
	run := runPlugins(t, "plugins: [valid, 123]\n")
	if run.err == nil {
		t.Fatal("invalid config accepted")
	}
	if run.calls != "" {
		t.Errorf("magpie ran for %q", run.calls)
	}
}

func TestPluginsInstalledSourceIsKept(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeAtomic(filepath.Join(root, "magpie/plugins.json"), []byte(`{"plugins": [{"spec": "kept@1.0.0"}]}`), 0o600)
	writeAtomic(filepath.Join(root, "team-worker-docker/plugin-sources.json"), []byte(`{"kept": "kept@1.0.0"}`), 0o600)
	t.Setenv("PATH", t.TempDir())
	path := writeFile(t, root, "plugin.yaml", `{"plugins": ["kept"]}`)
	failed, err := configurePlugins(path, io.Discard, io.Discard)
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed = %v, err = %v; an installed plugin must not be reinstalled", failed, err)
	}
}
