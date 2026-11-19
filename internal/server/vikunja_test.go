package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestDaemonVikunjaHook(t *testing.T) {
	dir, err := os.MkdirTemp("", "tw-vikunja-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	t.Setenv("HOME", dir)
	t.Setenv("TEAM_WORKER_SOCKET", socket)
	t.Setenv("TEAM_WORKER_SERVER_URL", "")
	t.Setenv("TEAM_WORKER_NO_AUTOSTART", "1")
	if err := os.WriteFile(filepath.Join(dir, "vikunja-hooks.json"),
		[]byte(`{"alpha":"alpha-example-secret","beta":{"secret":"beta-example-secret","events":["task.*"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	const alphaEvent = `{"event_name":"task.created","data":{"task":{"id":10}}}`
	const betaEvent = `{"event_name":"task.updated","data":{"task":{"id":11}}}`
	const projectEvent = `{"event_name":"project.updated","data":{"project":{"id":12}}}`
	st, err := store.Open(config.DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []store.CreateTeamInput{
		{Name: "alpha", LeaderName: "lead", User: "demo"},
		{Name: "beta", LeaderName: "other-lead"},
	} {
		if _, err := st.CreateTeam(ctx, input); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	if err := st.EnqueueVikunjaHook(ctx, "alpha", []byte(alphaEvent)); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- Run(runCtx, Options{DataDir: dir, Socket: socket, HTTPAddr: address, Board: true})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Daemon did not stop")
		}
	})
	api := client.New(dir)
	deadline := time.Now().Add(5 * time.Second)
	for api.Health(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatal("Daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	settings, err := api.VikunjaSettings(ctx)
	if err != nil || len(settings) != 2 || !settings[0].HasSecret || settings[0].Events != nil ||
		len(settings[1].Events) != 1 || settings[1].Events[0] != "task.*" {
		t.Fatalf("Unexpected initial webhook settings: %+v, %v", settings, err)
	}
	if _, err := api.SetVikunjaSettings(ctx, "alpha", model.VikunjaHookUpdate{Events: []string{"task*"}}); err == nil {
		t.Fatal("An invalid event wildcard was saved")
	}
	saved, err := api.SetVikunjaSettings(ctx, "alpha", model.VikunjaHookUpdate{Events: []string{"task.created"}})
	if err != nil || !saved.HasSecret || len(saved.Events) != 1 || saved.Events[0] != "task.created" {
		t.Fatalf("Unexpected saved settings: %+v, %v", saved, err)
	}

	httpClient := &http.Client{Timeout: 3 * time.Second}
	for _, test := range []struct {
		team, secret, body string
		want               int
	}{
		{"alpha", "", alphaEvent, http.StatusUnauthorized},
		{"beta", "alpha-example-secret", alphaEvent, http.StatusUnauthorized},
		{"unknown", "alpha-example-secret", alphaEvent, http.StatusNotFound},
		{"alpha", "alpha-example-secret", `{"event_name":"task.created"}`, http.StatusBadRequest},
		{"alpha", "alpha-example-secret", strings.Repeat(" ", vikunjaHookBodyLimit+1), http.StatusRequestEntityTooLarge},
		{"alpha", "alpha-example-secret", betaEvent, http.StatusNoContent},
		{"beta", "beta-example-secret", projectEvent, http.StatusNoContent},
		{"alpha", "alpha-example-secret", alphaEvent, http.StatusAccepted},
		{"beta", "beta-example-secret", betaEvent, http.StatusAccepted},
		{"beta", "beta-example-secret", betaEvent, http.StatusAccepted},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/hooks/vikunja/"+test.team, strings.NewReader(test.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if test.secret != "" {
			mac := hmac.New(sha256.New, []byte(test.secret))
			mac.Write([]byte(test.body))
			req.Header.Set("X-Vikunja-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != test.want {
			t.Fatalf("Webhook for %s returned %d, want %d", test.team, resp.StatusCode, test.want)
		}
	}
	for _, check := range []struct{ team, inbox, body string }{
		{"alpha", "demo_lead", alphaEvent},
		{"beta", "other-lead", betaEvent},
	} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			messages, err := api.Peek(ctx, check.team, check.inbox, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) > 0 {
				if len(messages) != 1 || messages[0].From != "vikunja" || !strings.HasSuffix(messages[0].Content, check.body) {
					t.Fatalf("Unexpected messages in %s/%s: %+v", check.team, check.inbox, messages)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("No webhook delivered to %s/%s", check.team, check.inbox)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, err := api.SetVikunjaSettings(ctx, "alpha", model.VikunjaHookUpdate{Events: []string{}}); err != nil {
		t.Fatal(err)
	}
	settings, err = api.VikunjaSettings(ctx)
	if err != nil || len(settings) != 2 || settings[0].Events == nil || len(settings[0].Events) != 0 {
		t.Fatalf("An empty allowlist was not preserved: %+v, %v", settings, err)
	}
}
