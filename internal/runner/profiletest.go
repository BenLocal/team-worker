package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/profile"
	"github.com/benlocal/team-worker/internal/spawn"
)

// TestProfile runs the profile's command non-interactively, the way a
// subprocess agent starts, with the profile environment applied.
func TestProfile(ctx context.Context, name, prompt, cwd string) (model.ProfileTest, error) {
	cfg, err := profile.Load(config.FilePath())
	if err != nil {
		return model.ProfileTest{}, err
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return model.ProfileTest{}, fmt.Errorf("Profile '%s' not found", name)
	}
	caller := os.Environ()
	applied, err := profile.Apply(p, nil, profile.CallerEnv(caller))
	if err != nil {
		return model.ProfileTest{}, err
	}
	registry, err := builtin.Load(config.AgentsDir())
	if err != nil {
		return model.ProfileTest{}, err
	}
	adapter := registry.For(applied.Command)
	plan := adapter.Prepare(agent.Session{Command: applied.Command, Prompt: prompt, SkipPermissions: true})
	env, err := spawn.ComposeEnv(spawn.EnvSpec{Caller: caller, Adapter: plan.Env, AdapterName: adapter.Name(),
		Layers: []spawn.EnvLayer{{Name: "profile " + name, Set: applied.Env}}})
	if err != nil {
		return model.ProfileTest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, plan.Args[0], plan.Args[1:]...)
	run.Dir = cwd
	for k, v := range env.Vars {
		run.Env = append(run.Env, k+"="+v)
	}
	if plan.Stdin != "" {
		run.Stdin = strings.NewReader(plan.Stdin)
	}
	var stdout, stderr bytes.Buffer
	run.Stdout, run.Stderr = &stdout, &stderr
	res := model.ProfileTest{Profile: name, Agent: applied.Agent, Command: HidePrompt(plan.Args, prompt)}
	err = run.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.ReturnCode = exitErr.ExitCode()
	case err != nil:
		return model.ProfileTest{}, fmt.Errorf("run %s: %w", plan.Args[0], err)
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	return res, nil
}
