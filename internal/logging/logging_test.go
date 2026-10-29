package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingKeepsMaxFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	r, err := OpenRotating(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	r.maxBytes = 100 // keep the test small
	line := bytes.Repeat([]byte("x"), 60)
	for i := 0; i < 5; i++ {
		if _, err := r.Write(append([]byte{byte('a' + i)}, line...)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	for _, name := range []string{"d.log", "d.log.1", "d.log.2"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("only MaxFiles rotated files may be kept")
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".2")
	if cur[0] != 'e' || old[0] != 'c' {
		t.Fatalf("rotation order: current starts %q, oldest starts %q", cur[0], old[0])
	}
}

func TestReopenContinuesSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	os.WriteFile(path, bytes.Repeat([]byte("y"), 90), 0o600)
	r, _ := OpenRotating(path, 1, 2)
	r.maxBytes = 100
	r.Write([]byte("0123456789abc"))
	r.Close()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("existing size must count toward the limit after a restart")
	}
}

func TestNewLevelAndFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.log")
	log, closer, err := New(Options{Level: "warn", Format: "json", File: path})
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hidden")
	log.Warn("shown", "k", "v")
	closer.Close()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "hidden") || !strings.Contains(string(data), `"msg":"shown"`) {
		t.Fatalf("log: %s", data)
	}
	if _, _, err := New(Options{Level: "loud"}); err == nil {
		t.Fatal("bad level must fail")
	}
	if _, _, err := New(Options{Format: "xml"}); err == nil {
		t.Fatal("bad format must fail")
	}
}
