package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
)

func (a *app) captureNativeSession(ctx context.Context, team, name, runID string) error {
	session, err := a.client().GetSession(ctx, team, name, config.IdentityFromEnv().User)
	if client.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.RunID != runID || session.SessionID != "" || session.Client != "codex" {
		return nil
	}
	rec, err := a.client().GetAgent(ctx, team, name)
	if err != nil {
		return err
	}
	started, err := time.Parse(time.RFC3339Nano, rec.StartedAt)
	if err != nil {
		return err
	}
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".codex")
	}
	id, err := agent.CaptureCodexSession(filepath.Join(root, "sessions"), session.Cwd, rec.AgentID, started)
	if err != nil || id == "" {
		return err
	}
	session.SessionID, session.Source = id, "transcript"
	_, err = a.client().SaveSession(ctx, session)
	return err
}
