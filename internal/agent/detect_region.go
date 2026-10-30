package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// screenRegion is the part of the screen a rule looks at; the names and
// their slicing follow herdr (src/detect/manifest.rs, region()).
type screenRegion struct {
	kind string
	n    int
}

func parseScreenRegion(s string) (screenRegion, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return screenRegion{kind: "whole_recent"}, nil
	}
	switch s {
	case "whole_recent", "osc_title", "osc_progress", "after_last_prompt_marker", "before_current_prompt_marker",
		"whole_recent_without_current_prompt_marker", "current_prompt_block_marker",
		"after_current_prompt_block_marker", "prompt_box_body", "above_prompt_box",
		"last_non_empty_above_prompt_box", "after_last_horizontal_rule":
		return screenRegion{kind: s}, nil
	}
	for _, kind := range []string{"bottom_lines", "bottom_non_empty_lines", "top_non_empty_lines"} {
		arg, ok := strings.CutPrefix(s, kind+"(")
		if !ok {
			continue
		}
		arg, ok = strings.CutSuffix(arg, ")")
		if !ok {
			break
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 {
			break
		}
		// herdr wants a canonical count here: no leading zero, at most 65535.
		if kind == "top_non_empty_lines" && (n == 0 || n > 65535 || arg != strconv.Itoa(n)) {
			break
		}
		return screenRegion{kind: kind, n: n}, nil
	}
	return screenRegion{}, fmt.Errorf("unknown region %q", s)
}

func (r screenRegion) extract(in ScreenInput) string {
	c := in.Screen
	switch r.kind {
	case "osc_title":
		return in.Title
	case "osc_progress":
		return in.Progress
	case "whole_recent":
		return c
	}
	lines := textLines(c)
	from := func(i int) string { return c[lineOffset(lines, c, i):] }
	switch r.kind {
	case "bottom_lines":
		return from(max(0, len(lines)-r.n))
	case "bottom_non_empty_lines":
		start, found := -1, 0
		for i := len(lines) - 1; i >= 0 && found < r.n; i-- {
			if !blank(lines[i]) {
				start, found = i, found+1
			}
		}
		if start < 0 {
			return ""
		}
		return from(start)
	case "top_non_empty_lines":
		end, found := -1, 0
		for i := 0; i < len(lines) && found < r.n; i++ {
			if !blank(lines[i]) {
				end, found = i, found+1
			}
		}
		if end < 0 {
			return ""
		}
		return c[:lineOffset(lines, c, end+1)]
	case "after_last_prompt_marker":
		for i := len(lines) - 1; i >= 0; i-- {
			if codexPrompt(lines[i]) {
				return from(i + 1)
			}
		}
		return c
	case "before_current_prompt_marker":
		if i := currentCodexPrompt(lines); i >= 0 {
			return c[:lineOffset(lines, c, i)]
		}
		return c
	case "whole_recent_without_current_prompt_marker":
		if currentCodexPrompt(lines) >= 0 {
			return ""
		}
		return c
	case "current_prompt_block_marker":
		if i := currentPromptBlockMarker(lines); i >= 0 {
			return lines[i]
		}
		return ""
	case "after_current_prompt_block_marker":
		if i := currentPromptBlockMarker(lines); i >= 0 {
			return from(i)
		}
		return ""
	case "prompt_box_body":
		top := promptBoxTop(lines)
		if top < 0 {
			return ""
		}
		start := lineOffset(lines, c, top+1)
		for i := top + 1; i < len(lines); i++ {
			if horizontalRule(lines[i]) {
				return c[start:lineOffset(lines, c, i)]
			}
		}
		return c[start:]
	case "above_prompt_box":
		return abovePromptBox(lines, c)
	case "last_non_empty_above_prompt_box":
		above := textLines(abovePromptBox(lines, c))
		for i := len(above) - 1; i >= 0; i-- {
			if !blank(above[i]) {
				return above[i]
			}
		}
		return ""
	case "after_last_horizontal_rule":
		end, offset := -1, 0
		for _, l := range lines {
			if horizontalRule(l) {
				end = offset + len(l) + 1
			}
			offset += len(l) + 1
		}
		if end < 0 {
			return c
		}
		return c[min(end, len(c)):]
	}
	return ""
}

// textLines splits like Rust's str::lines: on "\n", dropping one trailing
// "\r" per line and the empty element after a final "\n".
func textLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// lineOffset is the byte offset where line i starts, capped at len(c).
func lineOffset(lines []string, c string, i int) int {
	off := 0
	for k := 0; k < i && k < len(lines); k++ {
		off += len(lines[k]) + 1
	}
	return min(off, len(c))
}

func blank(l string) bool { return strings.TrimSpace(l) == "" }

// horizontalRule is a line of box-drawing dashes (U+2500): two or more on
// their own, or three or more followed by a title.
func horizontalRule(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" {
		return false
	}
	rest := strings.TrimLeft(t, "─")
	n := (len(t) - len(rest)) / len("─")
	if n == 0 {
		return false
	}
	return strings.TrimSpace(rest) == "" || n >= 3
}

// promptBoxTop is the second horizontal rule from the bottom: the top border
// of the input box Claude Code draws between two rules.
func promptBoxTop(lines []string) int {
	seen := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if horizontalRule(lines[i]) {
			if seen++; seen == 2 {
				return i
			}
		}
	}
	return -1
}

func abovePromptBox(lines []string, c string) string {
	if top := promptBoxTop(lines); top >= 0 {
		return c[:lineOffset(lines, c, top)]
	}
	return c
}

// codexPrompt is Codex's input line: "›" alone or followed by a space.
func codexPrompt(l string) bool { return l == "›" || strings.HasPrefix(l, "› ") }

func codexBlockMarker(l string) bool {
	for _, m := range []string{"•", "■", "✗", "✓"} {
		if strings.HasPrefix(l, m) {
			return true
		}
	}
	return false
}

// currentCodexPrompt is the last prompt line, unless output blocks follow it.
func currentCodexPrompt(lines []string) int {
	prompt := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if codexPrompt(lines[i]) {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		return -1
	}
	for _, l := range lines[prompt+1:] {
		if codexBlockMarker(l) {
			return -1
		}
	}
	return prompt
}

func currentPromptBlockMarker(lines []string) int {
	prompt := currentCodexPrompt(lines)
	if prompt < 0 {
		return -1
	}
	for i := prompt - 1; i >= 0; i-- {
		if codexBlockMarker(lines[i]) {
			return i
		}
	}
	return -1
}
