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
	if !strings.Contains(answer, "reply in Feishu") || !strings.Contains(answer, "task results") ||
		strings.Contains(answer, "## Summarize a result") {
		t.Fatal("Answer mode must receive its own prompt and the team-worker CLI guidance")
	}
	for _, test := range []struct {
		name, sessionKey, previousThread, mode string
		tool, approval, scoped, routeFormat    bool
		interactive                            string
	}{
		{name: "temporary thread for background work"},
		{name: "first conversation turn", sessionKey: "session-key"},
		{name: "resume conversation", sessionKey: "session-key", previousThread: "thread-1"},
		{name: "tools for a request unrelated to teams", mode: "answer", tool: true, sessionKey: "session-key", previousThread: "thread-1"},
		{name: "resumed answer uses route format", mode: "answer", sessionKey: "session-key", previousThread: "thread-1", routeFormat: true},
		{name: "tool invocation in intent classification", tool: true},
		{name: "team-scoped reply keeps restricted permissions", mode: "answer", scoped: true, tool: true},
		{name: "interactive approval", mode: "answer", approval: true},
		{name: "approve once and resume the turn", mode: "answer", interactive: "approval", previousThread: "thread-1"},
		{name: "submit form and resume the turn", mode: "answer", interactive: "input"},
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
				if mode != "answer" || test.scoped {
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
					case "initialize":
						if test.interactive != "" && !strings.Contains(string(request.Params), `"experimentalApi":true`) {
							t.Error("Interactive server requests were not enabled")
						}
					case "initialized":
						continue
					case "config/read":
						result = map[string]any{"config": map[string]any{}}
					case "thread/start", "thread/resume":
						var fields map[string]any
						if err := json.Unmarshal(request.Params, &fields); err != nil {
							t.Errorf("Invalid thread params: %v", err)
						}
						if (test.scoped || mode != "answer") && (fields["sandbox"] != "read-only" || fields["approvalPolicy"] != "never") {
							t.Errorf("Scoped thread must use restricted permissions, got %s", request.Params)
						}
						if !test.scoped && mode == "answer" && (fields["sandbox"] != "danger-full-access" ||
							fields["approvalPolicy"] != "on-request" || fields["approvalsReviewer"] != "user") {
							t.Errorf("Authorized answers must use Full Access with user approvals, got %s", request.Params)
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
						if mode == "answer" && (!strings.Contains(string(fields["outputSchema"]), `"summary"`) ||
							!strings.Contains(string(fields["input"]), "single string field summary") ||
							!strings.Contains(string(fields["input"]), "use the tools allowed by your profile")) {
							t.Errorf("The answer turn must state its output format: %s", request.Params)
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
				if test.interactive != "" {
					method := "item/commandExecution/requestApproval"
					params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "command": "pwd"}
					want := `{"decision":"accept"}`
					if test.interactive == "input" {
						method = "item/tool/requestUserInput"
						params["questions"] = []map[string]string{{"id": "topic", "header": "Topic", "question": "What should we work on?"}}
						want = `{"answers":{"topic":{"answers":["documentation"]}}}`
					}
					if err := out.Encode(map[string]any{"id": "server-99", "method": method, "params": params}); err != nil {
						finished <- err
						return
					}
					var response codexMessage
					if err := in.Decode(&response); err != nil || string(response.ID) != `"server-99"` || response.Method != "" || string(response.Result) != want {
						t.Errorf("Wrong response to the original server request: %+v, %v", response, err)
					}
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
				if test.routeFormat {
					text = `{"action":"answer","alias":"","team":"","leader":"","subject":"","owner":"","description":"Verified the result","runtime":""}`
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
			request := "Ask alpha"
			if mode == "answer" {
				request = "Read the document in your workspace and summarize it"
			}
			input := agentInput{Request: request, Teams: []string{"alpha"}, SessionKey: test.sessionKey, Scoped: test.scoped}
			if test.interactive != "" {
				input.interact = func(_ context.Context, i codexInteraction) (any, error) {
					choice := "accept"
					if test.interactive == "input" {
						choice = "submit"
					} else if i.text != "pwd" {
						t.Errorf("Approval card must show the command, got %q", i.text)
					}
					result, ok := i.response(choice, map[string]any{"answer_0": "documentation"})
					if !ok {
						return nil, errors.New("Invalid fixture answer")
					}
					return result, nil
				}
			}
			result, err := requestCodexTurn(ctx, client, client, "/fixture", "example-model", mode, test.previousThread, input)
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
