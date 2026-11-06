package model

import (
	"fmt"
	"strings"
	"time"
)

// DecisionStatus is where a decision request stands.
type DecisionStatus string

const (
	DecisionPending  DecisionStatus = "pending"
	DecisionApproved DecisionStatus = "approved"
	DecisionRejected DecisionStatus = "rejected"
	DecisionAnswered DecisionStatus = "answered"
)

// ValidDecisionStatus reports whether s names a decision request status.
func ValidDecisionStatus(s DecisionStatus) bool {
	return s == DecisionPending || s == DecisionApproved || s == DecisionRejected || s == DecisionAnswered
}

// DecisionOption is one choice a decision request offers, e.g. A.
type DecisionOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// AutoReviewer is the name the AI auto reviewer decides as; with a model it
// is "auto:<model>".
const AutoReviewer = "auto"

// IsAutoReviewer reports whether a decided-by name is the AI auto reviewer's.
func IsAutoReviewer(name string) bool {
	return name == AutoReviewer || strings.HasPrefix(name, AutoReviewer+":")
}

// DecisionRequest is a member's request for the operator to decide
// something, and the decision. The AI auto reviewer leaves HumanOnly
// requests alone unless the team's setting includes them, and then may only
// approve them.
type DecisionRequest struct {
	ID          string           `json:"id"`
	Team        string           `json:"team"`
	From        string           `json:"from"`
	Summary     string           `json:"summary"`
	Body        string           `json:"body,omitempty"`
	Options     []DecisionOption `json:"options"`
	Recommended string           `json:"recommended,omitempty"`
	HumanOnly   bool             `json:"humanOnly,omitempty"`
	Status      DecisionStatus   `json:"status"`
	Choice      string           `json:"choice,omitempty"`
	Reply       string           `json:"reply,omitempty"`
	DecidedBy   string           `json:"decidedBy,omitempty"`
	// AutoReason is the AI auto reviewer's rationale for its decision.
	AutoReason string `json:"autoReason,omitempty"`
	// AutoNote says why the AI auto reviewer left the request pending.
	AutoNote  string `json:"autoNote,omitempty"`
	CreatedAt string `json:"createdAt"`
	DecidedAt string `json:"decidedAt,omitempty"`
}

// OptionLabel returns the label of the option with key, or "".
func (r DecisionRequest) OptionLabel(key string) string {
	for _, o := range r.Options {
		if o.Key == key {
			return o.Label
		}
	}
	return ""
}

// AutoReview is a team's setting for the AI auto review of decision requests.
// From and To ("HH:MM") limit the review to a daily window that includes
// From and excludes To; a To before From crosses midnight. Both empty means
// all day. TimeZone is an IANA name, empty for the daemon's local time.
// IncludeHuman lets the reviewer approve human-only requests too.
type AutoReview struct {
	Enabled      bool   `json:"enabled"`
	Model        string `json:"model,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	TimeZone     string `json:"timeZone,omitempty"`
	IncludeHuman bool   `json:"includeHuman,omitempty"`
}

// HasWindow reports whether the review is limited to a daily window.
func (a AutoReview) HasWindow() bool { return a.From != "" || a.To != "" }

// Location is the window's time zone; empty is the local time zone.
func (a AutoReview) Location() (*time.Location, error) {
	if a.TimeZone == "" {
		return time.Local, nil
	}
	return time.LoadLocation(a.TimeZone)
}

// Active reports whether the auto review runs at now: it is on and now is
// inside the window. A window that does not parse is never active.
func (a AutoReview) Active(now time.Time) bool {
	if !a.Enabled {
		return false
	}
	if !a.HasWindow() {
		return true
	}
	from, err1 := ParseClock(a.From)
	to, err2 := ParseClock(a.To)
	loc, err3 := a.Location()
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	if from < to {
		return from <= m && m < to
	}
	return m >= from || m < to
}

// ParseClock reads a 24-hour "HH:MM" time as minutes after midnight.
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != len("15:04") {
		return 0, fmt.Errorf("invalid time %q: use HH:MM, 24-hour", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// AutoReviewStatus is a team's auto review setting as the daemon reports
// it, with whether it runs right now and the daemon's own time zone (what
// an empty TimeZone means).
type AutoReviewStatus struct {
	AutoReview
	Active     bool   `json:"active"`
	DaemonZone string `json:"daemonZone"`
}

// NewAutoReviewStatus reports a at now; the daemon zone is now's zone.
func NewAutoReviewStatus(a AutoReview, now time.Time) AutoReviewStatus {
	return AutoReviewStatus{AutoReview: a, Active: a.Active(now), DaemonZone: now.Format("MST -07:00")}
}
