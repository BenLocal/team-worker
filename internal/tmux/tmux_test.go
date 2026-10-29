package tmux

import (
	"os"
	"path/filepath"
	"testing"
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
			t.Setenv("PATH", dir)
			t.Setenv("TMUX_TEST_ERROR", tc.message)
			if err := KillPane("%0"); (err != nil) != tc.wantErr {
				t.Fatalf("KillPane error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
