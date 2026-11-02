package model

// AgentDiff is ClawTeam's context diff: what an agent's work changes
// relative to its base branch.
type AgentDiff struct {
	FilesChanged []string `json:"files_changed"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	DiffStat     string   `json:"diff_stat"`
	CommitCount  int      `json:"commit_count"`
	Summary      string   `json:"summary"`
	// UncommittedFiles counts the changed files in the agent's worktrees
	// that are not committed yet, which the rest of the diff leaves out.
	UncommittedFiles int `json:"uncommitted_files,omitempty"`
}

// FileOverlap is a file several agents changed. Severity is high when they
// changed the same lines, else medium.
type FileOverlap struct {
	File     string   `json:"file"`
	Agents   []string `json:"agents"`
	Severity string   `json:"severity"`
}

// AgentCommit is one commit in the team's cross-branch log.
type AgentCommit struct {
	Agent     string   `json:"agent"`
	Hash      string   `json:"hash"`
	Message   string   `json:"message"`
	Timestamp string   `json:"timestamp"`
	Files     []string `json:"files"`
}
