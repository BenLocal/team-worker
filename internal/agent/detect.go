package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ScreenState is what an agent's terminal shows it is doing.
type ScreenState string

const (
	ScreenIdle    ScreenState = "idle"
	ScreenWorking ScreenState = "working"
	ScreenBlocked ScreenState = "blocked"
	ScreenUnknown ScreenState = "unknown"
)

// ScreenInput is what a rule set reads: the screen as plain text (rows
// without trailing spaces, joined with "\n", ending in "\n"), the terminal
// title the CLI set (OSC 0/2) and its last OSC 9 progress report.
type ScreenInput struct {
	Screen   string
	Title    string
	Progress string
}

// Detection is the outcome of matching a screen against an agent's rules.
type Detection struct {
	State ScreenState
	// Rule is the id of the rule that decided; empty when no rule matched,
	// in which case State is the idle fallback and no Visible flag is set.
	Rule string
	// The Visible flags say the screen shows the state outright, e.g. the
	// input prompt itself, rather than it being inferred.
	VisibleIdle    bool
	VisibleBlocker bool
	VisibleWorking bool
	// SkipStateUpdate marks a viewer the CLI opened (a transcript, a model
	// picker); the screen says nothing about the agent's state.
	SkipStateUpdate bool
}

// ScreenRules decides an agent's state from its terminal. The format and
// semantics are those of herdr's agent detection manifests
// (https://github.com/herdrdev/herdr, src/detect/manifest.rs), so its
// manifests can be used unchanged.
type ScreenRules struct {
	id    string
	rules []screenRule
}

type screenRule struct {
	id             string
	state          ScreenState
	priority       int
	region         screenRegion
	visibleIdle    bool
	visibleBlocker bool
	visibleWorking bool
	skip           bool
	gate           *screenGate
}

type screenGate struct {
	contains  []string // lowercase
	regex     []*regexp.Regexp
	lineRegex []*regexp.Regexp
	all       []*screenGate
	any       []*screenGate
	not       []*screenGate
}

// Manifest limits, as herdr enforces them.
const (
	maxScreenRules    = 128
	maxGateDepth      = 8
	maxGates          = 512
	maxGateMatchers   = 32
	maxTotalMatchers  = 1024
	maxMatcherLength  = 512
	screenEngineLevel = 3
)

type manifestFile struct {
	ID               string         `toml:"id"`
	Version          string         `toml:"version"`
	MinEngineVersion *int           `toml:"min_engine_version"`
	UpdatedAt        string         `toml:"updated_at"`
	Aliases          []string       `toml:"aliases"`
	Rules            []manifestRule `toml:"rules"`
}

type manifestRule struct {
	ID              string         `toml:"id"`
	State           string         `toml:"state"`
	Priority        int            `toml:"priority"`
	Region          string         `toml:"region"`
	VisibleIdle     bool           `toml:"visible_idle"`
	VisibleBlocker  bool           `toml:"visible_blocker"`
	VisibleWorking  bool           `toml:"visible_working"`
	SkipStateUpdate bool           `toml:"skip_state_update"`
	All             []manifestGate `toml:"all"`
	Any             []manifestGate `toml:"any"`
	Not             []manifestGate `toml:"not"`
	Contains        []string       `toml:"contains"`
	Regex           []string       `toml:"regex"`
	LineRegex       []string       `toml:"line_regex"`
}

type manifestGate struct {
	All       []manifestGate `toml:"all"`
	Any       []manifestGate `toml:"any"`
	Not       []manifestGate `toml:"not"`
	Contains  []string       `toml:"contains"`
	Regex     []string       `toml:"regex"`
	LineRegex []string       `toml:"line_regex"`
}

// ParseScreenRules reads and validates a herdr detection manifest.
func ParseScreenRules(data []byte) (*ScreenRules, error) {
	var f manifestFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("unknown key %q", extra[0].String())
	}
	if f.MinEngineVersion != nil && *f.MinEngineVersion > screenEngineLevel {
		return nil, fmt.Errorf("needs engine version %d; this engine is %d", *f.MinEngineVersion, screenEngineLevel)
	}
	if len(f.Rules) == 0 || len(f.Rules) > maxScreenRules {
		return nil, fmt.Errorf("needs 1 to %d rules, has %d", maxScreenRules, len(f.Rules))
	}
	c := compiler{}
	sr := &ScreenRules{id: f.ID}
	for _, r := range f.Rules {
		rule, err := c.rule(r, f.MinEngineVersion)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		sr.rules = append(sr.rules, rule)
	}
	return sr, nil
}

// MustParseScreenRules is ParseScreenRules for rules built into the binary.
func MustParseScreenRules(name string, data []byte) *ScreenRules {
	r, err := ParseScreenRules(data)
	if err != nil {
		panic(fmt.Sprintf("screen rules %s: %v", name, err))
	}
	return r
}

type compiler struct {
	gates, matchers int
}

func (c *compiler) rule(r manifestRule, minEngine *int) (screenRule, error) {
	if strings.TrimSpace(r.ID) == "" {
		return screenRule{}, fmt.Errorf("rule id must not be blank")
	}
	state := ScreenState(r.State)
	switch state {
	case "":
		state = ScreenUnknown
	case ScreenIdle, ScreenWorking, ScreenBlocked, ScreenUnknown:
	default:
		return screenRule{}, fmt.Errorf("unknown state %q", r.State)
	}
	if r.SkipStateUpdate && (state != ScreenUnknown || r.VisibleIdle || r.VisibleBlocker || r.VisibleWorking) {
		return screenRule{}, fmt.Errorf("skip_state_update needs state = \"unknown\" and no visible_* flag")
	}
	region, err := parseScreenRegion(r.Region)
	if err != nil {
		return screenRule{}, err
	}
	if region.kind == "top_non_empty_lines" && minEngine != nil && *minEngine < 3 {
		return screenRule{}, fmt.Errorf("top_non_empty_lines needs min_engine_version 3")
	}
	g, err := c.gate(manifestGate{All: r.All, Any: r.Any, Not: r.Not, Contains: r.Contains, Regex: r.Regex,
		LineRegex: r.LineRegex}, 1, true)
	if err != nil {
		return screenRule{}, err
	}
	return screenRule{id: r.ID, state: state, priority: r.Priority, region: region, visibleIdle: r.VisibleIdle,
		visibleBlocker: r.VisibleBlocker, visibleWorking: r.VisibleWorking, skip: r.SkipStateUpdate, gate: g}, nil
}

// gate compiles one condition. positive gates (a rule and its all/any
// members) need a matcher of their own; a not gate may consist of not alone.
func (c *compiler) gate(m manifestGate, depth int, positive bool) (*screenGate, error) {
	if depth > maxGateDepth {
		return nil, fmt.Errorf("conditions nest deeper than %d", maxGateDepth)
	}
	if c.gates++; c.gates > maxGates {
		return nil, fmt.Errorf("more than %d conditions", maxGates)
	}
	direct := len(m.Contains) + len(m.Regex) + len(m.LineRegex)
	if direct > maxGateMatchers {
		return nil, fmt.Errorf("more than %d matchers in one condition", maxGateMatchers)
	}
	if c.matchers += direct; c.matchers > maxTotalMatchers {
		return nil, fmt.Errorf("more than %d matchers", maxTotalMatchers)
	}
	if positive && direct == 0 && len(m.All) == 0 && len(m.Any) == 0 {
		return nil, fmt.Errorf("a condition needs contains, regex, line_regex, all or any")
	}
	if !positive && direct == 0 && len(m.All) == 0 && len(m.Any) == 0 && len(m.Not) == 0 {
		return nil, fmt.Errorf("a not condition needs a matcher")
	}
	g := &screenGate{}
	for _, s := range m.Contains {
		if err := checkMatcher(s); err != nil {
			return nil, err
		}
		g.contains = append(g.contains, strings.ToLower(s))
	}
	for _, list := range []struct {
		src []string
		dst *[]*regexp.Regexp
	}{{m.Regex, &g.regex}, {m.LineRegex, &g.lineRegex}} {
		for _, s := range list.src {
			if err := checkMatcher(s); err != nil {
				return nil, err
			}
			re, err := regexp.Compile(goRegex(s))
			if err != nil {
				return nil, fmt.Errorf("regex %q: %w", s, err)
			}
			*list.dst = append(*list.dst, re)
		}
	}
	for _, sub := range []struct {
		src      []manifestGate
		dst      *[]*screenGate
		positive bool
	}{{m.All, &g.all, true}, {m.Any, &g.any, true}, {m.Not, &g.not, false}} {
		for _, s := range sub.src {
			cg, err := c.gate(s, depth+1, sub.positive)
			if err != nil {
				return nil, err
			}
			*sub.dst = append(*sub.dst, cg)
		}
	}
	return g, nil
}

func checkMatcher(s string) error {
	if len([]rune(s)) > maxMatcherLength {
		return fmt.Errorf("matcher longer than %d characters", maxMatcherLength)
	}
	return nil
}

var rustUnicodeEscape = regexp.MustCompile(`\\u\{([0-9A-Fa-f]{1,6})\}|\\u([0-9A-Fa-f]{4})`)

// goRegex rewrites the Rust regex syntax that Go's RE2 lacks: \uXXXX and
// \u{X} code points become \x{X}.
func goRegex(s string) string {
	return rustUnicodeEscape.ReplaceAllStringFunc(s, func(m string) string {
		sub := rustUnicodeEscape.FindStringSubmatch(m)
		hex := sub[1]
		if hex == "" {
			hex = sub[2]
		}
		return `\x{` + hex + `}`
	})
}

// Detect returns the state the highest-priority matching rule names; on a
// tie the earlier rule wins. Without a match the agent counts as idle, but
// with no Visible flag, so callers can tell evidence from the fallback.
func (r *ScreenRules) Detect(in ScreenInput) Detection {
	var best *screenRule
	for i := range r.rules {
		rule := &r.rules[i]
		text := rule.region.extract(in)
		if !rule.gate.matches(text, strings.ToLower(text)) {
			continue
		}
		if best == nil || rule.priority > best.priority {
			best = rule
		}
	}
	if best == nil {
		return Detection{State: ScreenIdle}
	}
	return Detection{
		State:           best.state,
		Rule:            best.id,
		VisibleIdle:     best.visibleIdle && best.state == ScreenIdle,
		VisibleBlocker:  best.visibleBlocker && best.state == ScreenBlocked,
		VisibleWorking:  best.visibleWorking && best.state == ScreenWorking,
		SkipStateUpdate: best.skip,
	}
}

// matches is the AND of every part; an empty part holds vacuously.
func (g *screenGate) matches(text, lower string) bool {
	for _, s := range g.contains {
		if !strings.Contains(lower, s) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(text) {
			return false
		}
	}
	if len(g.lineRegex) > 0 {
		lines := textLines(text)
		for _, re := range g.lineRegex {
			found := false
			for _, l := range lines {
				if re.MatchString(l) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	for _, sub := range g.all {
		if !sub.matches(text, lower) {
			return false
		}
	}
	if len(g.any) > 0 {
		found := false
		for _, sub := range g.any {
			if sub.matches(text, lower) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, sub := range g.not {
		if sub.matches(text, lower) {
			return false
		}
	}
	return true
}
