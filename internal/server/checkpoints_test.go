package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// This exercises real daemon startup, persisted recovery, API calls and shutdown.
func TestDaemonCheckpointRecoveryAndCompletion(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tw-checkpoint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	t.Setenv("HOME", dir)
	t.Setenv("TEAM_WORKER_SERVER_URL", "")
	t.Setenv("TEAM_WORKER_API_KEY", "")
	t.Setenv("TEAM_WORKER_SOCKET", socket)
	t.Setenv("TEAM_WORKER_NO_AUTOSTART", "1")
	api := client.New(dir)
	start := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, Options{DataDir: dir, Socket: socket}) }()
		stop := func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("daemon did not stop")
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for api.Health(context.Background()) != nil {
			if time.Now().After(deadline) {
				stop()
				t.Fatal("daemon did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return stop
	}
	stop := start()
	ctx := context.Background()
	if _, err := api.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		stop()
		t.Fatal(err)
	}
	task, err := api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "Observe", Owner: "leader"})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	// Schedule before restarting. Whether the first process sees it or not,
	// the new process must preserve one notification and the outstanding work.
	cp, err := api.CreateCheckpoint(ctx, "demo", store.CreateCheckpointInput{Key: "sample", TaskID: task.ID, DueAt: time.Now().Add(-time.Second), AckTimeoutSeconds: 60, ResultDueAt: time.Now().Add(time.Hour), EscalateAfterSeconds: 60, Action: "Read sample", ExpectedEvidence: "sample log"})
	stop()
	if err != nil {
		t.Fatal(err)
	}
	stop = start()
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cp, err = api.GetCheckpoint(ctx, "demo", cp.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cp.Status == "notified" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("checkpoint did not fire after restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cp, err = api.UpdateCheckpoint(ctx, "demo", cp.ID, store.CheckpointUpdateInput{Action: "ack", Caller: "leader"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err = api.UpdateCheckpoint(ctx, "demo", cp.ID, store.CheckpointUpdateInput{Action: "complete", Caller: "leader", Token: cp.Token, Outcome: "inconclusive", Summary: "Sample unavailable", Evidence: "artifacts/sample.log (build example)"})
	if err != nil || cp.Status != "completed" {
		t.Fatalf("completion: %+v %v", cp, err)
	}
	task, err = api.GetTask(ctx, "demo", task.ID)
	if err != nil || task.Status == model.StatusCompleted {
		t.Fatalf("checkpoint completed task: %+v %v", task, err)
	}
	list, err := api.ListCheckpoints(ctx, "demo")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
}
