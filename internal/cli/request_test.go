package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestDecisionRequestFlow(t *testing.T) {
	h := newHarness(t)
	h.json(&map[string]any{}, "team", "spawn-team", "demo")
	t.Setenv("TEAM_WORKER_AGENT_ID", "")

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	os.WriteFile(bodyFile, []byte("Three ways to lay out storage.\n"), 0o600)
	var created map[string]string
	h.json(&created, "request", "create", "demo", "Storage layout?", "--body-file", bodyFile,
		"--option", "A=keep as is", "--option", "B=move", "--option", "C=split", "--recommend", "C")
	id := created["requestId"]
	if created["status"] != "requested" || created["from"] != "leader" || len(id) != 12 {
		t.Fatalf("create: %v", created)
	}
	if out, ok := h.run("--json", "request", "create", "demo", "x", "--option", "no-label"); ok || !strings.Contains(out, "KEY=LABEL") {
		t.Fatalf("bad option: ok=%v %s", ok, out)
	}

	var operatorInbox []model.Message
	h.json(&operatorInbox, "inbox", "receive", "demo", "--agent", "operator")
	if len(operatorInbox) != 1 || operatorInbox[0].Type != "decision_request" || operatorInbox[0].RequestID != id {
		t.Fatalf("operator inbox: %+v", operatorInbox)
	}

	var listed []model.DecisionRequest
	h.json(&listed, "request", "list", "demo", "--status", "pending")
	if len(listed) != 1 || listed[0].ID != id || len(listed[0].Options) != 3 || listed[0].Body != "Three ways to lay out storage.\n" {
		t.Fatalf("list: %+v", listed)
	}
	if out, ok := h.run("request", "show", "demo", id); !ok || !strings.Contains(out, "Option:    C = split (recommended)") {
		t.Fatalf("show: %s", out)
	}

	if out, ok := h.run("--json", "request", "decide", "demo", id); ok || !strings.Contains(out, "choose one of") {
		t.Fatalf("decide without an action: ok=%v %s", ok, out)
	}
	if out, ok := h.run("--json", "request", "decide", "demo", id, "--reject"); ok || !strings.Contains(out, "needs a reason") {
		t.Fatalf("reject without a reason: ok=%v %s", ok, out)
	}
	// A spawned agent decides under its own name, which is refused.
	t.Setenv("TEAM_WORKER_AGENT_ID", "abc123")
	if out, ok := h.run("--json", "request", "decide", "demo", id, "--choice", "C"); ok || !strings.Contains(out, "decided by the operator") {
		t.Fatalf("agent decides: ok=%v %s", ok, out)
	}
	t.Setenv("TEAM_WORKER_AGENT_ID", "")
	var decided map[string]string
	h.json(&decided, "request", "decide", "demo", id, "--choice", "C", "-m", "go ahead")
	if decided["status"] != "approved" || decided["choice"] != "C" || decided["decidedBy"] != "operator" {
		t.Fatalf("decide: %v", decided)
	}
	var leaderInbox []model.Message
	h.json(&leaderInbox, "inbox", "receive", "demo")
	if len(leaderInbox) != 1 || leaderInbox[0].Type != "decision_result" || leaderInbox[0].Choice != "C" ||
		!strings.Contains(leaderInbox[0].Content, "Note: go ahead") {
		t.Fatalf("leader inbox: %+v", leaderInbox)
	}

	var setting model.AutoReview
	h.json(&setting, "request", "auto", "demo", "on", "--model", "group/x")
	if !setting.Enabled || setting.Model != "group/x" {
		t.Fatalf("auto on: %+v", setting)
	}
	h.json(&setting, "request", "auto", "demo", "off")
	if setting.Enabled || setting.Model != "group/x" {
		t.Fatalf("auto off keeps the model: %+v", setting)
	}
	if out, ok := h.run("request", "auto", "demo"); !ok || !strings.Contains(out, "off (model: group/x)") {
		t.Fatalf("auto show: %s", out)
	}
}
