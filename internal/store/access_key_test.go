package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAccessKeyPersistsAndUsesConfiguredOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.EnsureAccessKey(context.Background(), "")
	if err != nil || len(key) != 64 {
		t.Fatalf("generated key failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	again, err := s.EnsureAccessKey(context.Background(), "")
	if err != nil || again != key {
		t.Fatal("key changed across reopen")
	}
	override, err := s.EnsureAccessKey(context.Background(), "test-only-override")
	if err != nil || override != "test-only-override" {
		t.Fatal("override was not used")
	}
	saved, err := s.AccessKey(context.Background())
	if err != nil || saved != override {
		t.Fatal("override was not persisted")
	}
}
