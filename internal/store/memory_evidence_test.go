package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestMemoryTaskEvidenceExcludesAmbiguousOwners(t *testing.T) {
	st := newTestStore(t)
	newTeam(t, st, "worker")
	ctx := context.Background()
	task, err := st.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Verified task", Owner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted), Result: &model.TaskResult{Summary: "A reusable outcome", Verification: "Observed artifact"}}); err != nil {
		t.Fatal(err)
	}
	evidence, err := st.MemoryTaskEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := model.MemoryIdentity{Team: "demo", Name: "worker"}
	if !strings.Contains(evidence[id], "A reusable outcome") {
		t.Fatal(evidence)
	}
	if _, err = st.AddMember(ctx, "demo", AddMemberInput{Name: "worker", User: "other"}); err != nil {
		t.Fatal(err)
	}
	evidence, err = st.MemoryTaskEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 {
		t.Fatal("ambiguous task owner leaked across users", evidence)
	}
}
