package builtin

import "github.com/benlocal/team-worker/internal/agent"

// dsh is the built-in adapter for dsh (DeepSeek Harness).
func dsh() agent.Def {
	return agent.Def{
		Name: "dsh", Names: []string{"dsh"},
		// dsh has no bypass flag; this pins the permission mode for the session.
		SkipEnv:    map[string]string{"DSH_PERMISSION_MODE": "danger-full-access"},
		TmuxPrompt: arg, BatchPrompt: arg,
	}
}
