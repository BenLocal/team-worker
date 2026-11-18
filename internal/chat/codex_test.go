package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRoutingCodexAppServerProtocol(t *testing.T) {
	_, answer := teamAssistantPromptFor("answer")
	if !strings.Contains(answer, "Reply to the sender in Feishu") || !strings.Contains(answer, "task results") ||
		strings.Contains(answer, "## Summarize a result") {
		t.Fatal("Answer mode must receive its own prompt and the team-worker CLI guidance")
	}
	for _, test := range []struct {
		name, sessionKey, previousThread, mode string
		tool, approval, scoped                 bool
	}{
		{name: "temporary thread for background work"},
		{name: "first conversation turn", sessionKey: "session-key"},
		{name: "resume conversation", sessionKey: "session-key", previousThread: "thread-1"},
		{name: "tool events in a reply", mode: "answer", tool: true},
		{name: "tool invocation in intent classification", tool: true},
		{name: "team-scoped reply keeps restricted permissions", mode: "answer", scoped: true, tool: true},
		{name: "interactive approval", mode: "answer", approval: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode := test.mode
			if mode == "" {
				mode = "route"
			}
			client, server := net.Pipe()
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				defer server.Close()
				in, out := json.NewDecoder(server), json.NewEncoder(server)
				threadMethod := "thread/start"
				if test.previousThread != "" {
					threadMethod = "thread/resume"
				}
				expect := []string{"initialize", "initialized"}
				if test.scoped {
					expect = append(expect, "config/read")
				}
				expect = append(expect, threadMethod, "turn/start")
				for _, method := range expect {
					var request codexMessage
					if err := in.Decode(&request); err != nil {
						finished <- err
						return
					}
					if request.Method != method {
						t.Errorf("Sent %q, want %q", request.Method, method)
					}
					var result any = map[string]any{}
					switch method {
					case "initialized":
						continue
					case "config/read":
						result = map[string]any{"config": map[string]any{}}
					case "thread/start", "thread/resume":
						var fields map[string]any
						if err := json.Unmarshal(request.Params, &fields); err != nil {
							t.Errorf("Invalid thread params: %v", err)
						}
						if test.scoped && (fields["sandbox"] != "read-only" || fields["approvalPolicy"] != "never") {
							t.Errorf("Scoped thread must use restricted permissions, got %s", request.Params)
						}
						if !test.scoped && (fields["sandbox"] != nil || fields["approvalPolicy"] != nil) {
							t.Errorf("Thread must use configured permissions, got %s", request.Params)
						}
						if method == "thread/start" && fields["ephemeral"] != (test.sessionKey == "") {
							t.Errorf("Unexpected thread persistence: %s", request.Params)
						}
						if method == "thread/resume" && fields["threadId"] != test.previousThread {
							t.Errorf("Expected to resume %q, got %s", test.previousThread, request.Params)
						}
						if prompt, _ := fields["developerInstructions"].(string); !strings.Contains(prompt, "看下现在有几个team") || strings.Contains(prompt, "Summarize a result") {
							t.Errorf("The route turn did not receive its embedded prompt")
						}
						result = map[string]any{"thread": map[string]string{"id": "thread-1"}}
					case "turn/start":
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(request.Params, &fields); err != nil || len(fields["outputSchema"]) == 0 {
							t.Errorf("Expected a structured output schema: %v", err)
						}
						if mode == "route" && (!strings.Contains(string(fields["outputSchema"]), `"list_teams"`) ||
							!strings.Contains(string(fields["outputSchema"]), `"team_status"`) || !strings.Contains(string(fields["outputSchema"]), `"create_task"`)) {
							t.Errorf("The schema is missing assistant actions: %s", fields["outputSchema"])
						}
						result = map[string]any{"turn": map[string]string{"id": "turn-1"}}
						if err := out.Encode(map[string]any{"method": "item/started", "params": map[string]any{
							"threadId": "thread-1", "turnId": "turn-1", "item": map[string]string{"type": "userMessage"},
						}}); err != nil {
							finished <- err
							return
						}
					}
					if err := out.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
						finished <- err
						return
					}
				}
				if test.approval {
					finished <- out.Encode(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]string{
						"threadId": "thread-1", "turnId": "turn-1",
					}})
					return
				}
				if test.tool {
					if err := out.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
						"threadId": "thread-1", "turnId": "turn-1", "item": map[string]string{"type": "commandExecution"},
					}}); err != nil || mode != "answer" || test.scoped {
						finished <- err
						return
					}
				}
				text := `{"alias":"alpha"}`
				if mode == "answer" {
					text = `{"summary":"Verified the result"}`
				}
				if err := out.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
					"threadId": "thread-1", "turnId": "turn-1", "item": map[string]string{"type": "agentMessage", "text": text},
				}}); err != nil {
					finished <- err
					return
				}
				finished <- out.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "completed"},
				}})
			}()
			result, err := requestCodexTurn(ctx, client, client, "/fixture", "example-model", mode, test.previousThread,
				agentInput{Request: "Ask alpha", Teams: []string{"alpha"}, SessionKey: test.sessionKey, Scoped: test.scoped})
			switch {
			case test.approval:
				if !errors.Is(err, errCodexInteraction) {
					t.Fatalf("Expected an interactive approval error, got %v", err)
				}
			case test.tool && (mode == "route" || test.scoped):
				if !errors.Is(err, errCodexToolInInternalTurn) {
					t.Fatalf("Expected the internal turn to stop, got %v", err)
				}
			default:
				if err != nil || result.threadID != "thread-1" || (mode == "route" && result.Alias != "alpha") ||
					(mode == "answer" && result.Summary != "Verified the result") {
					t.Fatalf("Unexpected result %+v, %v", result, err)
				}
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}
