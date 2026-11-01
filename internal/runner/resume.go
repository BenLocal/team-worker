package runner

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

type ResumeRequest = model.ResumeRequest

// Resume starts an agent again on its saved session: the same CLI command,
// workspace and permissions, with its native session resumed.
func Resume(ctx context.Context, e Env, in ResumeRequest) (Result, error) {
	session, err := e.Client.GetSession(ctx, in.Team, in.Name, in.User)
	if err != nil {
		return Result{}, err
	}
	if session.SessionID == "" {
		return Result{}, fmt.Errorf("no native session id was captured; use session save first")
	}
	// A profile's variables come back from the profile, not the caller.
	profileLayers, err := ProfileEnvLayer(in.Profile, session.Command)
	if err != nil {
		return Result{}, err
	}
	layers := append(profileLayers, in.EnvLayers...)
	for _, key := range session.EnvKeys {
		_, supplied := os.LookupEnv(key)
		for _, layer := range layers {
			if _, ok := layer.Set[key]; ok {
				supplied = true
			}
		}
		if !supplied {
			return Result{}, fmt.Errorf("resume requires environment variable %s; supply it with --env or the daemon's environment", key)
		}
	}
	if in.KeepAlive != nil {
		session.KeepAlive = *in.KeepAlive
	}
	if in.MaxRestarts != nil {
		session.MaxRestarts = *in.MaxRestarts
	}
	if in.RestartDelay != nil {
		session.RestartDelayMillis = in.RestartDelay.Milliseconds()
	}
	var task string
	if in.Task != nil {
		task = *in.Task
	}
	return Spawn(ctx, e, Request{RequiredEnvKeys: session.EnvKeys, Team: in.Team, Name: in.Name, User: in.User,
		Task: task, TaskSet: in.Task != nil, Command: session.Command, Backend: session.Backend, Cwd: session.Cwd,
		SkipPerms: session.SkipPermissions, KeepAlive: session.KeepAlive, MaxRestarts: session.MaxRestarts,
		RestartDelay: time.Duration(session.RestartDelayMillis) * time.Millisecond, ResumeID: session.SessionID,
		Replace: in.Replace, DryRun: in.DryRun, EnvLayers: layers})
}
