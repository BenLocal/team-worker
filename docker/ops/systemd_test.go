package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEnvironmentEscapesForSystemd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.env")
	err := writeEnvironment(path, map[string]string{
		"B_VALUE":   "a\\b \"q\" `cmd` $HOME\nnext",
		"A_PLAIN":   "plain",
		"BAD-NAME":  "skipped",
		"1STARTNUM": "skipped",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "A_PLAIN=\"plain\"\n" +
		"B_VALUE=\"a\\\\b \\\"q\\\" \\`cmd\\` \\$HOME\nnext\"\n"
	if string(data) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestEnsureWebKeyIsStable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := ensureWebKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureWebKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first != second {
		t.Fatalf("keys %q and %q", first, second)
	}
}
