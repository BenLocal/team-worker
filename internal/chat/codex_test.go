package chat

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestRoutingCodexAppServerProtocol(t *testing.T) {
	for _, attemptTool := range []bool{false, true} {
		name := "structured result"
		if attemptTool {
			name = "tool event is rejected"
		}
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				defer server.Close()
				in, out := json.NewDecoder(server), json.NewEncoder(server)
				expect := []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"}
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
					case "thread/start":
						var fields map[string]any
						if err := json.Unmarshal(request.Params, &fields); err != nil || fields["ephemeral"] != true || fields["sandbox"] != "read-only" {
							t.Errorf("Expected ephemeral read-only thread, got %s: %v", request.Params, err)
						}
						result = map[string]any{"thread": map[string]string{"id": "thread-1"}}
					case "turn/start":
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(request.Params, &fields); err != nil || len(fields["outputSchema"]) == 0 {
							t.Errorf("Expected a structured output schema: %v", err)
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
				kind, typ := "item/completed", "agentMessage"
				if attemptTool {
					kind, typ = "item/started", "commandExecution"
				}
				if err := out.Encode(map[string]any{"method": kind, "params": map[string]any{
					"threadId": "thread-1", "turnId": "turn-1", "item": map[string]string{"type": typ, "text": `{"alias":"alpha"}`},
				}}); err != nil || attemptTool {
					finished <- err
					return
				}
				finished <- out.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "completed"},
				}})
			}()
			result, err := requestCodexTurn(ctx, client, client, "/fixture", "example-model", "route", agentInput{Request: "Ask alpha", Teams: []string{"alpha"}})
			if attemptTool && err == nil {
				t.Fatal("A tool invocation was accepted")
			}
			if !attemptTool && (err != nil || result.Alias != "alpha") {
				t.Fatalf("Routing result: %+v, %v", result, err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}
