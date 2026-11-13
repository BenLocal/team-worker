package model

// MemoryIdentity survives member and session deletion.
type MemoryIdentity struct {
	User string `json:"user"`
	Team string `json:"team"`
	Name string `json:"name"`
}

type MemoryDocument struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

type MemoryRevision struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	At      string `json:"at"`
	Source  string `json:"source"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

type MemoryStatus struct {
	Directory    string            `json:"directory"`
	Current      map[string]string `json:"current"`
	Provided     map[string]string `json:"provided"`
	Acknowledged map[string]string `json:"acknowledged"`
	RunID        string            `json:"runId"`
	Pending      bool              `json:"pending"`
}

type ReflectionConfig struct {
	AutoApply      bool     `json:"autoApply"`
	Enabled        bool     `json:"enabled"`
	Hour           int      `json:"hour"`
	Timezone       string   `json:"timezone"`
	Profile        string   `json:"profile,omitempty"`
	Command        []string `json:"command,omitempty"`
	Targets        []string `json:"targets"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

type MemoryEdit struct {
	Path     string `json:"path"`
	BaseHash string `json:"baseHash"`
	Content  string `json:"content"`
	Evidence string `json:"evidence"`
}

type Reflection struct {
	InputBytes     int          `json:"inputBytes"`
	DurationMillis int64        `json:"durationMillis"`
	ID             string       `json:"id"`
	At             string       `json:"at"`
	Status         string       `json:"status"`
	EvidenceHash   string       `json:"evidenceHash"`
	Edits          []MemoryEdit `json:"edits"`
	Error          string       `json:"error,omitempty"`
}

// MemoryRequest is used by the member's file-backed memory operations.
type MemoryRequest struct {
	Path     string            `json:"path,omitempty"`
	Content  string            `json:"content,omitempty"`
	BaseHash string            `json:"baseHash,omitempty"`
	Query    string            `json:"query,omitempty"`
	Revision string            `json:"revision,omitempty"`
	RunID    string            `json:"runId,omitempty"`
	Hashes   map[string]string `json:"hashes,omitempty"`
	Config   *ReflectionConfig `json:"config,omitempty"`
	DryRun   bool              `json:"dryRun,omitempty"`
}

type MemoryResult struct {
	Documents  []MemoryDocument  `json:"documents,omitempty"`
	History    []MemoryRevision  `json:"history,omitempty"`
	Context    string            `json:"context,omitempty"`
	Status     *MemoryStatus     `json:"status,omitempty"`
	Config     *ReflectionConfig `json:"config,omitempty"`
	Reflection *Reflection       `json:"reflection,omitempty"`
}
