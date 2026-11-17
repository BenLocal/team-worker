package model

// TaskTracking keeps execution history separate from the final result.
type TaskTracking struct {
	Kind         string         `json:"kind,omitempty"`
	ParentID     string         `json:"parentId,omitempty"`
	SupersededBy string         `json:"supersededBy,omitempty"`
	Progress     []TaskProgress `json:"progress,omitempty"`
}

type TaskProgress struct {
	Key          string   `json:"key"`
	Summary      string   `json:"summary"`
	State        string   `json:"state"`
	Reason       string   `json:"reason,omitempty"`
	NextAction   string   `json:"nextAction,omitempty"`
	CheckpointID string   `json:"checkpointId,omitempty"`
	RequestID    string   `json:"requestId,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Verification string   `json:"verification,omitempty"`
	ReportedAt   string   `json:"reportedAt"`
	ReportedBy   string   `json:"reportedBy,omitempty"`
}
