package agent

import (
	"strings"
	"testing"
)

func mustRules(t *testing.T, body string) *ScreenRules {
	t.Helper()
	r, err := ParseScreenRules([]byte("id = \"test\"\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScreenRuleSemantics(t *testing.T) {
	r := mustRules(t, `
[[rules]]
id = "first"
state = "working"
priority = 5
contains = ["BUSY"]

[[rules]]
id = "tie"
state = "blocked"
priority = 5
contains = ["busy"]

[[rules]]
id = "lines"
state = "blocked"
priority = 9
line_regex = ['^a$', '^b$']
not = [{ contains = ["skip"] }]
`)
	cases := []struct {
		screen string
		rule   string
		state  ScreenState
	}{
		// contains ignores case; on a priority tie the earlier rule wins.
		{"so busy\n", "first", ScreenWorking},
		// Each line_regex may match a different line; not vetoes the rule.
		{"a\nb\nbusy\n", "lines", ScreenBlocked},
		{"a\nb\nskip busy\n", "first", ScreenWorking},
		{"a\nc\n", "", ScreenIdle},
	}
	for _, c := range cases {
		if d := r.Detect(ScreenInput{Screen: c.screen}); d.Rule != c.rule || d.State != c.state {
			t.Errorf("%q: got %+v, want rule %q state %s", c.screen, d, c.rule, c.state)
		}
	}
}

func TestScreenRegions(t *testing.T) {
	screen := "head\n\nreply\n──── box ────\n❯ typed\n────────\n  footer\n\n"
	for _, c := range []struct{ region, want string }{
		{"bottom_non_empty_lines(2)", "────────\n  footer\n\n"},
		{"top_non_empty_lines(2)", "head\n\nreply\n"},
		{"prompt_box_body", "❯ typed\n"},
		{"last_non_empty_above_prompt_box", "reply"},
		{"after_last_horizontal_rule", "  footer\n\n"},
		{"bottom_lines(1)", "\n"},
	} {
		reg, err := parseScreenRegion(c.region)
		if err != nil {
			t.Fatal(err)
		}
		if got := reg.extract(ScreenInput{Screen: screen}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.region, got, c.want)
		}
	}
	codex := "› first\n• answer\n› second\n"
	for _, c := range []struct{ region, want string }{
		{"after_last_prompt_marker", ""},
		{"before_current_prompt_marker", "› first\n• answer\n"},
		{"whole_recent_without_current_prompt_marker", ""},
		{"current_prompt_block_marker", "• answer"},
	} {
		reg, _ := parseScreenRegion(c.region)
		if got := reg.extract(ScreenInput{Screen: codex}); got != c.want {
			t.Errorf("codex %s: got %q, want %q", c.region, got, c.want)
		}
	}
	for _, l := range []string{"──", "─── Title ───", "  ────  "} {
		if !horizontalRule(l) {
			t.Errorf("%q should be a horizontal rule", l)
		}
	}
	for _, l := range []string{"─ x", "---", "━━━", ""} {
		if horizontalRule(l) {
			t.Errorf("%q should not be a horizontal rule", l)
		}
	}
}

func TestScreenRulesValidation(t *testing.T) {
	bad := map[string]string{
		"unknown key":      "[[rules]]\nid = \"x\"\ncontains = [\"a\"]\nwhatever = 1\n",
		"no rules":         "",
		"bad region":       "[[rules]]\nid = \"x\"\nregion = \"middle\"\ncontains = [\"a\"]\n",
		"no matcher":       "[[rules]]\nid = \"x\"\nstate = \"idle\"\n",
		"only not":         "[[rules]]\nid = \"x\"\nnot = [{ contains = [\"a\"] }]\n",
		"skip with state":  "[[rules]]\nid = \"x\"\nstate = \"idle\"\nskip_state_update = true\ncontains = [\"a\"]\n",
		"bad regex":        "[[rules]]\nid = \"x\"\nregex = ['(']\n",
		"top count zero":   "[[rules]]\nid = \"x\"\nregion = \"top_non_empty_lines(0)\"\ncontains = [\"a\"]\n",
		"top leading zero": "[[rules]]\nid = \"x\"\nregion = \"top_non_empty_lines(07)\"\ncontains = [\"a\"]\n",
	}
	for name, body := range bad {
		if _, err := ParseScreenRules([]byte("id = \"test\"\n" + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Rust's \u escapes become Go's \x{...}.
	r := mustRules(t, "[[rules]]\nid = \"braille\"\nstate = \"working\"\nline_regex = ['^[\\u2800-\\u28FF]+ ', '\\u{25D0}']\n")
	if d := r.Detect(ScreenInput{Screen: "⠋ thinking ◐\n"}); d.State != ScreenWorking {
		t.Errorf("unicode escapes: %+v", d)
	}
	if !strings.Contains(goRegex(`\u{1F600}x\u00e9`), `\x{1F600}x\x{00e9}`) {
		t.Errorf("goRegex: %s", goRegex(`\u{1F600}x\u00e9`))
	}
}
