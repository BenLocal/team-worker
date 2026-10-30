package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJoinRequestApproveFlow(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")

	var pending map[string]any
	h.json(&pending, "team", "request-join", "demo", "alice", "-c", "writes Go", "--no-wait")
	id, _ := pending["requestId"].(string)
	if pending["status"] != "pending" || pending["teamName"] != "demo" || pending["proposedName"] != "alice" || !strings.HasPrefix(id, "join-") {
		t.Fatalf("pending = %v", pending)
	}
	var list []map[string]any
	h.json(&list, "team", "join-requests", "demo", "--status", "pending")
	if len(list) != 1 || list[0]["capabilities"] != "writes Go" {
		t.Fatalf("list = %v", list)
	}

	// Only the leader decides.
	t.Setenv("TEAM_WORKER_AGENT_NAME", "intruder")
	if out, ok := h.run("--json", "team", "approve-join", "demo", id); ok || !strings.Contains(out, "Only the team leader") {
		t.Fatalf("approve by non-leader: ok=%v %s", ok, out)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")

	var approved map[string]any
	h.json(&approved, "team", "approve-join", "demo", id, "--assigned-name", "alice-dev")
	if approved["status"] != "approved" || approved["assignedName"] != "alice-dev" || approved["teamName"] != "demo" || approved["agentId"] == "" {
		t.Fatalf("approved = %v", approved)
	}
	var status map[string]any
	h.json(&status, "team", "join-status", "demo", id)
	if status["status"] != "approved" || status["agentId"] != approved["agentId"] {
		t.Fatalf("status = %v", status)
	}
	var team map[string]any
	h.json(&team, "team", "status", "demo")
	if !strings.Contains(mustJSON(t, team), `"alice-dev"`) {
		t.Fatalf("team = %v", team)
	}
}

func TestJoinRequestWaitEndsOnDecisionOrTimeout(t *testing.T) {
	old := joinPollInterval
	joinPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { joinPollInterval = old })
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")

	decide := func(action string, extra ...string) {
		go func() {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				reqs, err := h.api.ListJoinRequests(t.Context(), "demo", "pending")
				if err == nil && len(reqs) == 1 {
					in := joinDecision()
					in.Caller, in.Reason = "leader", strings.Join(extra, "")
					if action == "approve" {
						h.api.ApproveJoin(t.Context(), "demo", reqs[0].RequestID, in)
					} else {
						h.api.RejectJoin(t.Context(), "demo", reqs[0].RequestID, in)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	decide("approve")
	out, ok := h.run("--json", "team", "request-join", "demo", "bob", "-t", "5")
	if !ok || !strings.Contains(out, `"status": "approved"`) || !strings.Contains(out, `"assignedName": "bob"`) {
		t.Fatalf("approved wait: ok=%v %s", ok, out)
	}

	decide("reject", "no room")
	out, ok = h.run("--json", "team", "request-join", "demo", "carol", "-t", "5")
	if ok || !strings.Contains(out, `"status": "rejected"`) || !strings.Contains(out, `"reason": "no room"`) {
		t.Fatalf("rejected wait should exit 1 with the reason: ok=%v %s", ok, out)
	}

	out, ok = h.run("--json", "team", "request-join", "demo", "dave", "-t", "1")
	if !ok || !strings.Contains(out, `"status": "pending"`) {
		t.Fatalf("timeout should print the pending request and exit 0: ok=%v %s", ok, out)
	}
	if out, ok := h.run("--json", "team", "request-join", "nope", "eve", "--no-wait"); ok || !strings.Contains(out, "Team 'nope' not found") {
		t.Fatalf("missing team: ok=%v %s", ok, out)
	}
	if out, ok := h.run("--json", "team", "reject-join", "demo", "join-000000000000"); ok || !strings.Contains(out, "No join request found") {
		t.Fatalf("unknown request: ok=%v %s", ok, out)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
