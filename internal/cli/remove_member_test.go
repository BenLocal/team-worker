package cli

import (
	"strings"
	"testing"
)

func TestTeamRemoveMember(t *testing.T) {
	h := newHarness(t)
	if out, ok := h.run("team", "spawn-team", "demo"); !ok {
		t.Fatal(out)
	}
	if out, ok := h.run("team", "add-member", "demo", "alice"); !ok {
		t.Fatal(out)
	}
	if out, ok := h.run("task", "create", "demo", "work", "-o", "alice"); !ok {
		t.Fatal(out)
	}
	if out, ok := h.run("--json", "team", "remove-member", "demo", "alice"); ok || !strings.Contains(out, "reassign") {
		t.Fatalf("a member with unfinished tasks must stay: %s", out)
	}
	if out, ok := h.run("team", "add-member", "demo", "bob"); !ok {
		t.Fatal(out)
	}
	var removed struct {
		Status string `json:"status"`
	}
	h.json(&removed, "team", "remove-member", "demo", "bob")
	if removed.Status != "removed" {
		t.Fatalf("remove-member: %+v", removed)
	}
	out, ok := h.run("team", "remove-member", "demo", "bob", "--force")
	if !ok || !strings.Contains(out, "not found") {
		t.Fatalf("a second removal: %s", out)
	}
	out, _ = h.run("team", "status", "demo")
	if strings.Contains(out, "bob") {
		t.Fatalf("bob is still listed:\n%s", out)
	}
}
