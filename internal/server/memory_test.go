package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/memory"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestScheduledReflectionPersistsAndDoesNotRepeat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, nil)
	srv.SetRunner(dir, "")
	files := srv.memoryFiles()
	id := model.MemoryIdentity{Team: "demo", Name: "worker", User: "example"}
	if err = files.Ensure(id); err != nil {
		t.Fatal(err)
	}
	if _, err = files.Write(id, "daily/2026-01-02.md", "user: Record this confirmed correction.", "", "test"); err != nil {
		t.Fatal(err)
	}
	doc, err := files.Read(id, "MEMORY.md")
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(map[string]any{"edits": []model.MemoryEdit{{Path: doc.Path, BaseHash: doc.Hash, Content: "# Memory\nConfirmed correction.\n", Evidence: "daily/2026-01-02.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "reflect")
	if err = os.WriteFile(command, []byte("#!/bin/sh\nif [ -n \"$TEAM_WORKER_AGENT_NAME\" ]; then exit 7; fi\ncat <<'RESULT'\n"+string(output)+"\nRESULT\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := model.ReflectionConfig{Enabled: true, AutoApply: true, Hour: 0, Timezone: "UTC", Command: []string{command}, Targets: []string{"MEMORY.md"}, TimeoutSeconds: 5}
	if _, err = files.ReflectionState(id, &cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "must-not-leak")
	now := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
	if err = srv.memoryTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	state, err := files.ReflectionState(id, nil)
	if err != nil || state.Latest.Status != "pending" {
		t.Fatalf("%+v %v", state, err)
	}
	// A fresh server sees the candidate on disk and applies it while stopped.
	srv = New(st, nil)
	srv.SetRunner(dir, "")
	if err = srv.memoryTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	state, err = srv.memoryFiles().ReflectionState(id, nil)
	if err != nil || state.Latest.Status != "applied" {
		t.Fatalf("%+v %v", state, err)
	}
	if err = srv.memoryTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	state, err = srv.memoryFiles().ReflectionState(id, nil)
	if err != nil || state.Attempts != 1 {
		t.Fatalf("repeated reflection: %+v %v", state, err)
	}
	got, err := files.Read(id, "MEMORY.md")
	if err != nil || got.Content != "# Memory\nConfirmed correction.\n" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReflectionDoesNotTrustExitedRecordWhileProcessLives(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "worker"}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sleep", "30")
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	if _, err = st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "worker", RunID: "example-run", Backend: "subprocess", PID: command.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.AgentExitedRun(ctx, "demo", "worker", "example-run", 0); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)
	srv.SetRunner(dir, "")
	files := srv.memoryFiles()
	id := model.MemoryIdentity{Team: "demo", Name: "worker"}
	if err = files.Ensure(id); err != nil {
		t.Fatal(err)
	}
	doc, err := files.Read(id, "MEMORY.md")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := files.BeginReflection(id, "user: verified fact", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(map[string]any{"edits": []model.MemoryEdit{{Path: doc.Path, BaseHash: doc.Hash, Content: "verified fact", Evidence: "user correction"}}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := files.FinishReflection(id, job, string(output), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = srv.applyReflection(ctx, id, candidate.ID, false); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("live process accepted: %v", err)
	}
	current, err := files.Read(id, doc.Path)
	if err != nil || current.Hash != doc.Hash {
		t.Fatalf("changed while alive: %+v %v", current, err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("killed process unexpectedly succeeded")
	}
	if _, err = srv.applyReflection(ctx, id, candidate.ID, false); err != nil {
		t.Fatal(err)
	}
}
