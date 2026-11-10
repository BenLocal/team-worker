package store

import (
	"context"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestTaskResultUpgradeAndValidation(t *testing.T) {
	path := legacyDB(t, 19)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.GetTask(ctx, "legacy", "t1")
	if err != nil || task.Result != nil {
		t.Fatalf("legacy result: %+v, %v", task, err)
	}
	_, err = s.UpdateTask(ctx, "legacy", "t1", UpdateTaskInput{Status: ptr(model.StatusCompleted), Result: &model.TaskResult{Summary: " "}})
	if KindOf(err) != KindInvalid {
		t.Fatalf("empty report accepted: %v", err)
	}
	task, err = s.GetTask(ctx, "legacy", "t1")
	if err != nil || task.Status != model.StatusPending {
		t.Fatalf("invalid report changed status: %+v, %v", task, err)
	}
	task, err = s.UpdateTask(ctx, "legacy", "t1", UpdateTaskInput{Result: &model.TaskResult{Summary: "Checked report", Artifacts: []string{"report.txt"}, ReportedAt: "untrusted"}})
	if err != nil || task.Result == nil || task.Result.ReportedAt == "untrusted" {
		t.Fatalf("report: %+v, %v", task, err)
	}
	_, err = s.UpdateTask(ctx, "legacy", "t1", UpdateTaskInput{Result: &model.TaskResult{Summary: "Corrected report"}})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(ctx, "legacy", TaskFilter{})
	if err != nil || len(tasks) != 1 || tasks[0].Result == nil || tasks[0].Result.Summary != "Corrected report" || len(tasks[0].Result.Artifacts) != 0 {
		t.Fatalf("replacement: %+v, %v", tasks, err)
	}
}
