package model

// Workspace is one agent's git worktree of one repository. The JSON field
// names are ClawTeam's WorkspaceInfo.
type Workspace struct {
	AgentName    string `json:"agent_name"`
	AgentID      string `json:"agent_id"`
	TeamName     string `json:"team_name"`
	BranchName   string `json:"branch_name"`
	WorktreePath string `json:"worktree_path"`
	RepoRoot     string `json:"repo_root"`
	BaseBranch   string `json:"base_branch"`
	CreatedAt    string `json:"created_at"`
}

// WorkspaceRepo is what an operation did in one of an agent's repositories.
type WorkspaceRepo struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status,omitempty"`
	Output string `json:"output,omitempty"`
}

// WorkspaceOp reports a workspace operation. Status, team, agent and output
// are ClawTeam's fields; Repos breaks the result down by repository.
type WorkspaceOp struct {
	Status string          `json:"status"`
	Team   string          `json:"team"`
	Agent  string          `json:"agent,omitempty"`
	Count  int             `json:"count,omitempty"`
	Output string          `json:"output,omitempty"`
	Repos  []WorkspaceRepo `json:"repos,omitempty"`
}

// WorkspaceRequest selects an operation's repository and options. Repo
// limits it to one of the agent's repositories; Agent is used by cleanup,
// which covers the whole team without it.
type WorkspaceRequest struct {
	Agent     string `json:"agent,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Message   string `json:"message,omitempty"`
	Target    string `json:"target,omitempty"`
	NoCleanup bool   `json:"noCleanup,omitempty"`
}
