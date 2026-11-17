package model

// ProtocolState tracks delivery and declared reading, not behavioral compliance.
type ProtocolState struct {
	Version   string `json:"version"`
	OfferedAt string `json:"offeredAt"`
	ReadAt    string `json:"readAt,omitempty"`
	AckedAt   string `json:"ackedAt,omitempty"`
}

type ProtocolContext struct {
	Version string         `json:"version"`
	Content string         `json:"content"`
	State   *ProtocolState `json:"state,omitempty"`
}

type ProtocolRequest struct {
	Action  string `json:"action"`
	RunID   string `json:"runId"`
	Version string `json:"version,omitempty"`
}
