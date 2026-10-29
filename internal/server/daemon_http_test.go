package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/store"
)

func TestDaemonHTTPAndSocketShareData(t *testing.T) {
	for _, configured := range []string{"", "test-only-key"} {
		t.Run("configured="+configured, func(t *testing.T) { daemonKeyFlow(t, configured) })
	}
}

func daemonKeyFlow(t *testing.T, configured string) {
	dir, err := os.MkdirTemp("/tmp", "tw-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("TEAM_WORKER_SERVER_URL", "")
	t.Setenv("TEAM_WORKER_API_KEY", "")
	t.Setenv("TEAM_WORKER_SOCKET", filepath.Join(dir, "api.sock"))
	t.Setenv("TEAM_WORKER_NO_AUTOSTART", "1")
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{DataDir: dir, Socket: filepath.Join(dir, "api.sock"), HTTPAddr: address, APIKey: configured})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	local := client.New(dir)
	deadline := time.Now().Add(5 * time.Second)
	for local.Health(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	key, err := local.AccessKey(ctx)
	if err != nil || key == "" {
		t.Fatalf("local access key unavailable: %v", err)
	}
	if configured != "" && key != configured {
		t.Fatal("configured key was not preserved")
	}
	remote, err := client.NewRemote("http://"+address, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.AccessKey(ctx); err == nil {
		t.Fatal("HTTP exposed access key")
	}
	if _, err := remote.CreateTeam(ctx, store.CreateTeamInput{Name: "remote", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.GetTeam(ctx, "remote"); err != nil {
		t.Fatalf("socket without key: %v", err)
	}
	for _, key := range []string{"", "wrong-key"} {
		unauthorized, err := client.NewRemote("http://"+address, key)
		if err != nil {
			t.Fatal(err)
		}
		err = unauthorized.Health(ctx)
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %v", err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "api.sock"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
}
