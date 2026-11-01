package cli

import (
	"context"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func TestWatchStopEndsChildOnRequest(t *testing.T) {
	old := stopPollInterval
	stopPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { stopPollInterval = old })
	for _, rec := range []model.Agent{{RunID: "run-1", StopRequested: true}, {RunID: "run-2"}} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go watchStop(ctx, done, cancel, func() (model.Agent, error) { return rec, nil }, "run-1")
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("%+v: child was not cancelled", rec)
		}
		close(done)
	}
	// A running record that still owns the run keeps the child alive.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go watchStop(ctx, done, cancel, func() (model.Agent, error) { return model.Agent{RunID: "run-1"}, nil }, "run-1")
	time.Sleep(50 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("child cancelled without a stop request")
	}
	close(done)
}
