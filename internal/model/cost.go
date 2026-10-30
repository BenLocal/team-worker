package model

// CostEvent is one usage record: a manual report, or the running totals of
// one native session (Source "session"), which later captures replace.
type CostEvent struct {
	ID           string  `json:"id"`
	AgentName    string  `json:"agentName"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostCents    float64 `json:"costCents"`
	Source       string  `json:"source"`
	SessionID    string  `json:"sessionId,omitempty"`
	ReportedAt   string  `json:"reportedAt"`
}

// TokenCount is the tokens one agent used.
type TokenCount struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// CostSummary totals a team's usage. ByAgent holds cents as in ClawTeam;
// ByAgentTokens adds tokens, which automatic capture records without cents.
type CostSummary struct {
	TeamName          string                `json:"teamName"`
	TotalCostCents    float64               `json:"totalCostCents"`
	TotalInputTokens  int64                 `json:"totalInputTokens"`
	TotalOutputTokens int64                 `json:"totalOutputTokens"`
	ByAgent           map[string]float64    `json:"byAgent"`
	ByAgentTokens     map[string]TokenCount `json:"byAgentTokens"`
	EventCount        int                   `json:"eventCount"`
}

// CostView is `cost show`. budget_cents stays snake_case for ClawTeam.
type CostView struct {
	Summary       CostSummary `json:"summary"`
	BudgetCents   float64     `json:"budget_cents"`
	BudgetEnforce bool        `json:"budgetEnforce"`
	Events        []CostEvent `json:"events"`
}

// Budget is a team's spending limit and how far it has been used.
// Exhausted is true only when the limit is enforced and reached.
type Budget struct {
	Team        string  `json:"team"`
	BudgetCents float64 `json:"budgetCents"`
	Enforce     bool    `json:"enforce"`
	SpentCents  float64 `json:"spentCents"`
	Exhausted   bool    `json:"exhausted"`
}
