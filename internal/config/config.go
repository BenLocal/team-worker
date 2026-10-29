// Package config resolves file locations and the calling agent's identity.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	envDataDir = "TEAM_WORKER_DATA_DIR"
	envSocket  = "TEAM_WORKER_SOCKET"
)

// File is the optional ~/.team-worker/config.json.
type File struct {
	DataDir string `json:"data_dir"`
	User    string `json:"user"`
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// FilePath is fixed and never moves with data_dir.
func FilePath() string { return filepath.Join(homeDir(), ".team-worker", "config.json") }

func Load() File {
	var f File
	if data, err := os.ReadFile(FilePath()); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	return f
}

// DataDir returns TEAM_WORKER_DATA_DIR, then config data_dir, then ~/.team-worker.
func DataDir() string {
	if d := os.Getenv(envDataDir); d != "" {
		return d
	}
	if d := Load().DataDir; d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".team-worker")
}

func SocketPath(dataDir string) string {
	if s := os.Getenv(envSocket); s != "" {
		return s
	}
	return filepath.Join(dataDir, "team-workerd.sock")
}

func DBPath(dataDir string) string   { return filepath.Join(dataDir, "team-worker.db") }
func LockPath(dataDir string) string { return filepath.Join(dataDir, "team-workerd.lock") }
func LogPath(dataDir string) string  { return filepath.Join(dataDir, "team-workerd.log") }

// OutPath receives an autostarted daemon's stdout and stderr: startup
// errors and crashes, which rotation of the log file must not move away.
func OutPath(dataDir string) string { return filepath.Join(dataDir, "team-workerd.out") }

// WorkspaceDir is the directory a spawned agent works in unless --cwd is given.
func WorkspaceDir(dataDir, team, agent string) string {
	return filepath.Join(dataDir, "workspaces", team, agent)
}

// AgentLogPath holds the output of a subprocess-backend agent.
func AgentLogPath(dataDir, team, agent string) string {
	return filepath.Join(dataDir, "logs", team, agent+".log")
}

// AgentsDir holds TOML agent adapters that add or adjust agent CLIs.
func AgentsDir() string { return filepath.Join(homeDir(), ".team-worker", "agents") }

// StartBlockedFile optionally delays supervised children until a setup marker is removed.
func StartBlockedFile() string { return os.Getenv("TEAM_WORKER_START_BLOCKED_FILE") }

// TemplateDir holds launch templates.
func TemplateDir() string { return filepath.Join(homeDir(), ".team-worker", "templates") }

// Identity describes the agent running a CLI command, taken from the
// TEAM_WORKER_* variables that spawn sets for each agent.
type Identity struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	User      string `json:"user"`
	AgentType string `json:"agentType"`
	TeamName  string `json:"teamName"`
	IsLeader  bool   `json:"isLeader"`
}

func IdentityFromEnv() Identity {
	user := os.Getenv("TEAM_WORKER_USER")
	if user == "" {
		user = Load().User
	}
	return Identity{
		AgentID:   os.Getenv("TEAM_WORKER_AGENT_ID"),
		AgentName: envOr("TEAM_WORKER_AGENT_NAME", "agent"),
		User:      user,
		AgentType: envOr("TEAM_WORKER_AGENT_TYPE", "general-purpose"),
		TeamName:  os.Getenv("TEAM_WORKER_TEAM_NAME"),
		IsLeader:  isTrue(os.Getenv("TEAM_WORKER_AGENT_LEADER")),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}
