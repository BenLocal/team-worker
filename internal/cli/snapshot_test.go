package cli

import (
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestTeamSnapshotRestoreThroughCLI(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("task", "create", "demo", "keep me")
	var meta model.SnapshotMeta
	h.json(&meta, "team", "snapshot", "demo", "--tag", "v1")
	if meta.TaskCount != 1 || meta.Tag != "v1" || !strings.HasSuffix(meta.ID, "-v1") {
		t.Fatalf("%+v", meta)
	}
	h.run("task", "create", "demo", "drop me")

	var list []model.SnapshotMeta
	h.json(&list, "team", "snapshots", "demo")
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("%+v", list)
	}

	var dry model.RestoreSummary
	h.json(&dry, "team", "restore", "demo", meta.ID, "--dry-run")
	if !dry.DryRun || dry.Tasks != 1 {
		t.Fatalf("%+v", dry)
	}
	// Without --force and --json, the restore asks first; "n" aborts.
	if out, ok := h.runIn("n\n", "team", "restore", "demo", meta.ID); ok {
		t.Fatalf("restore went ahead without confirmation: %s", out)
	}
	var tasks []model.Task
	h.json(&tasks, "task", "list", "demo")
	if len(tasks) != 2 {
		t.Fatalf("aborted restore changed tasks: %+v", tasks)
	}
	if out, ok := h.runIn("y\n", "team", "restore", "demo", meta.ID); !ok || !strings.Contains(out, "Restored") {
		t.Fatalf("%v %s", ok, out)
	}
	h.json(&tasks, "task", "list", "demo")
	if len(tasks) != 1 || tasks[0].Subject != "keep me" {
		t.Fatalf("after restore: %+v", tasks)
	}

	var del map[string]string
	h.json(&del, "team", "snapshot-delete", "demo", meta.ID)
	if del["status"] != "deleted" {
		t.Fatalf("%v", del)
	}
	if out, ok := h.run("--json", "team", "snapshot-delete", "demo", meta.ID); ok || !strings.Contains(out, "not found") {
		t.Fatalf("second delete: %v %s", ok, out)
	}
}
