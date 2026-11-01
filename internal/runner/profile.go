package runner

import (
	"fmt"
	"os"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/profile"
	"github.com/benlocal/team-worker/internal/spawn"
)

// Profiles live in the daemon's config.json and read their key variables
// from the daemon's environment, where the agents start.

// ApplyProfile resolves the runtime profile for a spawn and applies it: the
// command (when none was given) gets the profile's agent, model and args,
// and its environment becomes the first layer after the adapter, below the
// template, template agent and --env layers.
func ApplyProfile(req *Request, explicit string) error {
	hasCommand := len(req.Command) > 0
	cfg, err := profile.Load(config.FilePath())
	if err != nil {
		return err
	}
	name, err := profile.Resolve(cfg, explicit, hasCommand)
	if err != nil {
		return err
	}
	if name == "" {
		if !hasCommand {
			req.Command = []string{"claude"}
		}
		return nil
	}
	applied, err := profile.Apply(cfg.Profiles[name], req.Command, profile.CallerEnv(os.Environ()))
	if err != nil {
		return fmt.Errorf("profile '%s': %w", name, err)
	}
	req.Command = applied.Command
	req.EnvLayers = append([]spawn.EnvLayer{{Name: "profile " + name, Set: applied.Env}}, req.EnvLayers...)
	return nil
}

// ProfileEnvLayer rebuilds a profile's environment layer for a saved command,
// for `agent resume`, which keeps the command it saved.
func ProfileEnvLayer(name string, command []string) ([]spawn.EnvLayer, error) {
	if name == "" {
		return nil, nil
	}
	cfg, err := profile.Load(config.FilePath())
	if err != nil {
		return nil, err
	}
	if _, err := profile.Resolve(cfg, name, true); err != nil {
		return nil, err
	}
	applied, err := profile.Apply(cfg.Profiles[name], command, profile.CallerEnv(os.Environ()))
	if err != nil {
		return nil, err
	}
	return []spawn.EnvLayer{{Name: "profile " + name, Set: applied.Env}}, nil
}
