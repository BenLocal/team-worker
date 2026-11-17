package model

import (
	"testing"
	"time"
)

func TestMemberWorkDoesNotConfuseAssignmentsWithExecution(t *testing.T) {
	now := time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	cp := &Checkpoint{ID: "cp", DueAt: now.Add(time.Hour), Status: "scheduled"}
	task := Task{ID: "observe", Owner: "alice", Status: StatusInProgress, NextCheckpoint: cp}
	w := SummarizeMemberWork([]Task{task}, "alice", now)
	if w.State != "needs_review" || len(w.Unreported) != 1 || w.NextAt == "" {
		t.Fatalf("future observation was assumed to cover all work: %+v", w)
	}
	task.ExecutionState = "waiting"
	task.WaitCheckpoint = cp
	task.Progress = []TaskProgress{{Key: "wait", State: "waiting"}}
	w = SummarizeMemberWork([]Task{task}, "alice", now)
	if w.State != "waiting" || w.ReadyTasks != 0 {
		t.Fatalf("registered wait was treated as executable: %+v", w)
	}
	w = SummarizeMemberWork([]Task{task}, "alice", now.Add(2*time.Hour))
	if w.State != "assigned" || w.ReadyTasks != 1 {
		t.Fatalf("due observation stayed asleep: %+v", w)
	}
	task.Status = StatusSuperseded
	w = SummarizeMemberWork([]Task{task}, "alice", now)
	if w.State != "standby" {
		t.Fatalf("closed round kept member busy: %+v", w)
	}
	task.Status = StatusInProgress
	task.Kind = "goal"
	if w = SummarizeMemberWork([]Task{task}, "alice", now); w.State != "standby" {
		t.Fatalf("goal counted as execution: %+v", w)
	}
}

func TestMemberWorkSeparatesDependenciesAndUnregisteredWaits(t *testing.T) {
	tasks := []Task{{ID: "deps", Owner: "alice", Status: StatusBlocked, BlockedBy: []string{"build"}}}
	if w := SummarizeMemberWork(tasks, "alice", time.Now()); w.State != "waiting" {
		t.Fatalf("dependency wait missing: %+v", w)
	}
	tasks = append(tasks, Task{ID: "unknown", Owner: "alice", Status: StatusInProgress, ExecutionState: "unregistered"})
	if w := SummarizeMemberWork(tasks, "alice", time.Now()); w.State != "needs_review" || len(w.Unreported) != 1 {
		t.Fatalf("missing trigger hidden: %+v", w)
	}
}
