package memory

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func TestReflectionCandidateConflictApplyAndReplay(t *testing.T) {
	m := New(t.TempDir())
	id := testIdentity()
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	d := mustRead(t, m, id, "MEMORY.md")
	job, _, err := m.BeginReflection(id, "user: remember this verified fact", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(map[string]any{"edits": []model.MemoryEdit{{Path: d.Path, BaseHash: d.Hash, Content: "# Memory\nVerified fact.\n", Evidence: "user's explicit correction"}}})
	candidate, err := m.FinishReflection(id, job, string(output), nil)
	if err != nil || candidate.Status != "pending" {
		t.Fatalf("%+v %v", candidate, err)
	}
	changed, err := m.Write(id, d.Path, "concurrent edit", d.Hash, "test")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = m.ApplyReflection(id, candidate.ID)
	if !errors.Is(err, ErrConflict) || candidate.Status != "conflict" {
		t.Fatalf("%+v %v", candidate, err)
	}
	if got := mustRead(t, m, id, d.Path); got.Content != "concurrent edit" {
		t.Fatal(got)
	}
	if _, err = m.Write(id, d.Path, d.Content, changed.Hash, "test"); err != nil {
		t.Fatal(err)
	}
	candidate, err = m.ApplyReflection(id, candidate.ID)
	if err != nil || candidate.Status != "applied" {
		t.Fatalf("%+v %v", candidate, err)
	}
	if _, err = m.ApplyReflection(id, candidate.ID); err != nil {
		t.Fatal(err)
	}
	job, skipped, err := m.BeginReflection(id, "user: remember this verified fact", false, time.Now())
	if err != nil || job.ID != "" || skipped.Status != "skipped" {
		t.Fatalf("replay: %+v %+v %v", job, skipped, err)
	}
}

func TestReflectionRejectsUnsupportedEditsAndBoundsRetries(t *testing.T) {
	m := New(t.TempDir())
	id := testIdentity()
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	cfg := defaultReflectionConfig()
	cfg.Enabled = true
	cfg.Hour = 0
	if _, err := m.ReflectionState(id, &cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		job, _, err := m.BeginReflection(id, "user: evidence", true, now)
		if err != nil || job.ID == "" {
			t.Fatal(err)
		}
		out, err := m.FinishReflection(id, job, `{"edits":[{"path":"../AGENTS.md","content":"bad","evidence":"x"}]}`, nil)
		if err == nil || out.Status != "failed" {
			t.Fatalf("%+v %v", out, err)
		}
	}
	job, _, err := m.BeginReflection(id, "user: evidence", true, now)
	if err != nil || job.ID != "" {
		t.Fatalf("unbounded retries: %+v %v", job, err)
	}
	job, _, err = m.BeginReflection(id, "user: evidence", true, now.Add(24*time.Hour))
	if err != nil || job.ID == "" {
		t.Fatalf("next day: %+v %v", job, err)
	}
}
