package model

import "strings"

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
// something, and the decision. HumanOnly requests are never decided by the
// AI auto reviewer.
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
type AutoReview struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
}
