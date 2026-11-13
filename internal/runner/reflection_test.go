package runner

import (
	"context"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func TestReflectionCommandHonorsCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Reflect(ctx, model.ReflectionConfig{Command: []string{"sh", "-c", "sleep 30"}}, "Return an empty edits array.")
	if err == nil {
		t.Fatal("cancelled reflection succeeded")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("reflection outlived its cancellation")
	}
}
