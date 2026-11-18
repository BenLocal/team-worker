package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutingAgentGatewayContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("An empty gateway key must not send an authorization header")
		}
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"example-model","detail":{"max_model_len":4096}}]}`)
		case "/v1/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["caller"] != "team-worker" || body["enable_thinking"] != false ||
				body["model"] != "example-model" || body["max_tokens"] != float64(1024) || body["tools"] != nil {
				t.Errorf("Unexpected gateway request: %+v", body)
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"alias\":\"alpha\"}"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("LLM_API_BASE", server.URL+"/v1")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("TEAM_WORKER_FEISHU_ROUTER_MODEL", "example-model")
	t.Setenv("TEAM_WORKER_FEISHU_ROUTER_ENABLE_THINKING", "false")
	agent := configuredGatewayAgent()
	if agent == nil {
		t.Fatal("Routing agent was not configured")
	}
	out, err := agent(t.Context(), "route", agentInput{Request: "Ask alpha", Teams: []string{"alpha", "beta"}})
	if err != nil || out.Alias != "alpha" {
		t.Fatalf("Unexpected routing result: %+v, %v", out, err)
	}
}
