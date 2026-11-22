package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKillPaneAlreadyGone(t *testing.T) {
	for _, tc := range []struct {
		message string
		wantErr bool
	}{
		{"can't find pane: %0", false},
		{"no server running on /tmp/tmux/default", false},
		{"error connecting to /tmp/tmux/default (No such file or directory)", false},
		{"error connecting to /tmp/tmux/default (Permission denied)", true},
	} {
		t.Run(tc.message, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TMUX_TEST_ERROR\" >&2\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TMUX_TEST_ERROR", tc.message)
			if err := KillPane("%0"); (err != nil) != tc.wantErr {
				t.Fatalf("KillPane error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestRunTimesOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := CommandTimeout
	CommandTimeout = 100 * time.Millisecond
	t.Cleanup(func() { CommandTimeout = orig })

	start := time.Now()
	_, err := Screen("%0")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Screen error = %v, want timeout error", err)
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("Screen took too long to time out: %s", time.Since(start))
	}
}
