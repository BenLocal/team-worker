package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setupMaskingHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MAGPIE_WEB_KEY", "web-key")
}

func TestMaskingTurnsOnOnceAndKeepsOtherSettings(t *testing.T) {
	setupMaskingHome(t)
	f := newFakeLibrary(t)
	f.respond("GET /api/settings", `{"theme":"dark","textSize":110}`)
	f.respond("POST /api/settings", `{"theme":"dark","textSize":110,"redact":true}`)

	if err := enableMasking(f.server.URL); err != nil {
		t.Fatal(err)
	}
	body, _ := f.bodies["/api/settings"].(map[string]any)
	if body["redact"] != true || body["theme"] != "dark" || body["textSize"] != float64(110) {
		t.Fatalf("saved settings = %v", body)
	}
	if _, err := os.Stat(maskingMarker()); err != nil {
		t.Fatalf("marker: %v", err)
	}

	// Once marked, a user who turned masking off keeps it off.
	f.posts = nil
	f.respond("GET /api/settings", `{"theme":"dark"}`)
	if err := enableMasking(f.server.URL); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 0 {
		t.Fatalf("second start posted %v", f.posts)
	}
}

func TestMaskingAlreadyOnOnlyWritesMarker(t *testing.T) {
	setupMaskingHome(t)
	f := newFakeLibrary(t)
	f.respond("GET /api/settings", `{"redact":true}`)
	if err := enableMasking(f.server.URL); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 0 {
		t.Fatalf("posted %v though masking was on", f.posts)
	}
	if _, err := os.Stat(maskingMarker()); err != nil {
		t.Fatalf("marker: %v", err)
	}
}

func TestMaskingNotKeptIsAnErrorAndRetriesNextStart(t *testing.T) {
	setupMaskingHome(t)
	f := newFakeLibrary(t)
	f.respond("GET /api/settings", `{}`)
	f.respond("POST /api/settings", `{}`)
	if err := enableMasking(f.server.URL); err == nil {
		t.Fatal("expected an error when Magpie drops redact")
	}
	if _, err := os.Stat(maskingMarker()); !os.IsNotExist(err) {
		t.Fatalf("marker written after a failed save: %v", err)
	}
}

func TestMaskingCanBeSkipped(t *testing.T) {
	setupMaskingHome(t)
	t.Setenv("TEAM_WORKER_MASKING", "0")
	cmd := maskingCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(maskingMarker()); !os.IsNotExist(err) {
		t.Fatalf("marker written while skipped: %v", err)
	}
}
