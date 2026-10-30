package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/board"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/store"
)

func TestDaemonServesBoardBesideAPI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tw-board-")
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
	reserved.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{DataDir: dir, Socket: filepath.Join(dir, "api.sock"), HTTPAddr: address,
			Board: true, BoardLinks: []board.Link{{Name: "Docs", URL: "https://example.com"}}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	local := client.New(dir)
	for deadline := time.Now().Add(5 * time.Second); local.Health(ctx) != nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
	}
	key, _ := local.AccessKey(ctx)
	if _, err := local.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}

	get := func(path, bearer string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+address+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	if code, body := get("/", ""); code != 200 || !strings.Contains(body, "Access key") {
		t.Fatalf("sign-in page: %d %.80q", code, body)
	}
	if code, _ := get("/api/teams", ""); code != 401 {
		t.Fatalf("board API without a key: %d", code)
	}
	// The board reads the API in-process, with the socket's rights.
	if code, body := get("/api/teams", key); code != 200 || !strings.Contains(body, `"demo"`) {
		t.Fatalf("board teams: %d %.120q", code, body)
	}
	if code, body := get("/api/links", key); code != 200 || !strings.Contains(body, "https://example.com") {
		t.Fatalf("board links: %d %q", code, body)
	}
	if code, _ := get("/v1/teams", ""); code != 401 {
		t.Fatalf("API without a key: %d", code)
	}
	if code, body := get("/v1/teams", key); code != 200 || !strings.Contains(body, `"demo"`) {
		t.Fatalf("API beside the board: %d %.120q", code, body)
	}
}

func TestBoardNeedsHTTPAddress(t *testing.T) {
	err := Run(context.Background(), Options{DataDir: t.TempDir(), Board: true})
	if err == nil || !strings.Contains(err.Error(), "--listen") {
		t.Fatalf("want a --listen error, got %v", err)
	}
}
