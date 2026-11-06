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
	"github.com/benlocal/team-worker/internal/model"
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
	send := func(method, path, contentType, origin, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", contentType)
		if origin != "" {
			req.Header.Set("Origin", origin)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	decision, err := local.CreateDecisionRequest(ctx, "demo", store.CreateDecisionInput{From: "leader", Summary: "Pick",
		Options: []model.DecisionOption{{Key: "A", Label: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	decide := "/api/teams/demo/requests/" + decision.ID + "/decide"
	if code, _ := send("POST", decide, "text/plain", "", `{"action":"approve","choice":"A"}`); code != 415 {
		t.Fatalf("board decide without JSON: %d", code)
	}
	if code, _ := send("POST", decide, "application/json", "http://evil.example", `{"action":"approve","choice":"A"}`); code != 403 {
		t.Fatalf("board decide cross-origin: %d", code)
	}
	if code, body := send("POST", decide, "application/json", "", `{"action":"approve","choice":"A","decidedBy":"leader"}`); code != 200 ||
		!strings.Contains(body, `"decidedBy":"operator"`) {
		t.Fatalf("board decide: %d %s", code, body)
	}
	if code, body := send("PUT", "/api/teams/demo/auto-review", "application/json", "", `{"enabled":true,"model":"m1"}`); code != 200 ||
		!strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"active":true`) {
		t.Fatalf("board auto review: %d %s", code, body)
	}
	// A window of the whole day but the minute after now is active now; one
	// minute far from now is not.
	now := time.Now().In(time.UTC)
	from, to := now.Add(2*time.Minute).Format("15:04"), now.Add(time.Minute).Format("15:04")
	window := `"from":"` + from + `","to":"` + to + `","timeZone":"UTC"`
	if code, body := send("PUT", "/api/teams/demo/auto-review", "application/json", "", `{"enabled":true,"model":"m1",`+window+`}`); code != 200 ||
		!strings.Contains(body, window+`,"active":true,"daemonZone":"`) {
		t.Fatalf("board auto review window: %d %s", code, body)
	}
	if code, body := get("/api/teams/demo/oversight", key); code != 200 || !strings.Contains(body, `"requests":[{"id":"`+decision.ID) ||
		!strings.Contains(body, `"autoReview":{"enabled":true,"model":"m1",`+window+`,"active":true,"daemonZone":"`) {
		t.Fatalf("board oversight: %d %.300s", code, body)
	}
	far := `"from":"` + now.Add(2*time.Hour).Format("15:04") + `","to":"` + now.Add(2*time.Hour+time.Minute).Format("15:04") + `","timeZone":"UTC"`
	if code, body := send("PUT", "/api/teams/demo/auto-review", "application/json", "", `{"enabled":true,`+far+`}`); code != 200 ||
		!strings.Contains(body, far+`,"active":false`) {
		t.Fatalf("board auto review outside the window: %d %s", code, body)
	}
	if code, body := send("PUT", "/api/teams/demo/auto-review", "application/json", "", `{"enabled":true,"from":"22:00","to":"22:00"}`); code != 400 ||
		!strings.Contains(body, "must differ") {
		t.Fatalf("board auto review empty window: %d %s", code, body)
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
