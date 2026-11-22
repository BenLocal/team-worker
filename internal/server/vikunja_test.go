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
	const plainComment = `{"event_name":"task.comment.created","data":{"task":{"id":20},"comment":{"comment":"/commanda Check the report"}}}`
	const richComment = `{"event_name":"task.comment.created","data":{"task":{"id":21},"comment":{"comment":"<p>/commandb&nbsp;Read the checklist</p>"}}}`
	const editedComment = `{"event_name":"task.comment.edited","data":{"task":{"id":22},"comment":{"comment":"/commanda Check the report"}}}`
	const mentionedCommand = `{"event_name":"task.comment.created","data":{"task":{"id":23},"comment":{"comment":"The /commanda example"}}}`
	const quotedCommand = `{"event_name":"task.comment.created","data":{"task":{"id":24},"comment":{"comment":"<blockquote><p>/commanda</p></blockquote>"}}}`
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
	if err := st.EnqueueVikunjaHook(ctx, "alpha", []byte(alphaEvent), ""); err != nil {
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
	if _, err := api.SetVikunjaSettings(ctx, "alpha", model.VikunjaHookUpdate{Commands: map[string]string{"invalid": "Inspect"}}); err == nil {
		t.Fatal("A command without a leading slash was saved")
	}
	const promptA = "Summarize the open questions."
	const promptB = "Review the acceptance criteria."
	saved, err := api.SetVikunjaSettings(ctx, "alpha", model.VikunjaHookUpdate{
		Events:   []string{"task.created", "task.comment.*"},
		Commands: map[string]string{"/commanda": promptA, "/commandb": promptB},
	})
	if err != nil || !saved.HasSecret || len(saved.Events) != 2 || saved.Commands["/commandb"] != promptB {
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
		{"alpha", "alpha-example-secret", plainComment, http.StatusAccepted},
		{"alpha", "alpha-example-secret", richComment, http.StatusAccepted},
		{"alpha", "alpha-example-secret", richComment, http.StatusAccepted},
		{"alpha", "alpha-example-secret", editedComment, http.StatusAccepted},
		{"alpha", "alpha-example-secret", mentionedCommand, http.StatusAccepted},
		{"alpha", "alpha-example-secret", quotedCommand, http.StatusAccepted},
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
	const prefix = "Vikunja webhook event (external data):\n"
	for _, check := range []struct {
		team, inbox string
		messages    []string
	}{
		{"alpha", "demo_lead", []string{
			prefix + alphaEvent,
			"Vikunja comment command /commanda\n\n" + promptA + "\n\n" + prefix + plainComment,
			"Vikunja comment command /commandb\n\n" + promptB + "\n\n" + prefix + richComment,
			prefix + editedComment, prefix + mentionedCommand, prefix + quotedCommand,
		}},
		{"beta", "other-lead", []string{prefix + betaEvent}},
	} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			messages, err := api.Peek(ctx, check.team, check.inbox, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) >= len(check.messages) {
				if len(messages) != len(check.messages) {
					t.Fatalf("Unexpected messages in %s/%s: %+v", check.team, check.inbox, messages)
				}
				for i, want := range check.messages {
					if messages[i].From != "vikunja" || messages[i].Content != want {
						t.Fatalf("Unexpected message in %s/%s: %+v", check.team, check.inbox, messages[i])
					}
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
	if err != nil || len(settings) != 2 || settings[0].Events == nil || len(settings[0].Events) != 0 ||
		settings[0].Commands["/commanda"] != promptA {
		t.Fatalf("An empty allowlist or existing commands were not preserved: %+v, %v", settings, err)
	}
}
