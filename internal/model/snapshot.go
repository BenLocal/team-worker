package model

// SnapshotMeta describes a saved team snapshot; field names follow
// ClawTeam's SnapshotMeta.
type SnapshotMeta struct {
	ID             string `json:"id"`
	TeamName       string `json:"teamName"`
	Tag            string `json:"tag"`
	CreatedAt      string `json:"createdAt"`
	MemberCount    int    `json:"memberCount"`
	TaskCount      int    `json:"taskCount"`
	EventCount     int    `json:"eventCount"`
	SessionCount   int    `json:"sessionCount"`
	CostEventCount int    `json:"costEventCount"`
	// MessageCount and PlanCount are additions: unread messages and plans.
	MessageCount int `json:"messageCount"`
	PlanCount    int `json:"planCount"`
}

// RestoreSummary is what `team restore` replaced or, with dry_run, would
// replace; field names follow ClawTeam.
type RestoreSummary struct {
	SnapshotID string `json:"snapshot_id"`
	DryRun     bool   `json:"dry_run"`
	Config     bool   `json:"config"`
	Tasks      int    `json:"tasks"`
	Events     int    `json:"events"`
	Sessions   int    `json:"sessions"`
	Costs      int    `json:"costs"`
	Inboxes    int    `json:"inboxes"`
	Plans      int    `json:"plans"`
	Members    int    `json:"members"`
}
