package cli

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
)

// localIdentity are the variables spawn gives an agent. An operator acting on
// a remote deployment must not carry a local agent's identity there.
var localIdentity = []string{
	"TEAM_WORKER_AGENT_ID", "TEAM_WORKER_AGENT_TYPE", "TEAM_WORKER_AGENT_LEADER", "TEAM_WORKER_TEAM_NAME",
	"TEAM_WORKER_USER", "TEAM_WORKER_PLAN_MODE_REQUIRED", "TEAM_WORKER_SOCKET", "TEAM_WORKER_DATA_DIR",
	"TEAM_WORKER_RUN_ID", "TEAM_WORKER_WORKSPACE_DIR",
}

// localOnly are commands that start agents on this machine, which an
// operator of a remote deployment never wants: the agent would run here,
// without the deployment's workspace, sessions or credentials.
var localOnly = [][]string{{"spawn"}, {"launch"}, {"agent", "resume"}, {"harness", "conduct"}}

// useRemote turns this process into an operator of the named remote: it
// points the client at the remote with its key, takes the remote's sender
// name instead of any local agent identity, never autostarts a daemon and,
// unless the remote says otherwise, bypasses the proxy for its host.
func useRemote(name string, cmd *cobra.Command) error {
	remotes := config.Load().Remotes
	r, ok := remotes[name]
	if !ok {
		names := make([]string, 0, len(remotes))
		for n := range remotes {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("no remote '%s' in %s (configured: %s)", name, config.FilePath(), strings.Join(names, ", "))
	}
	u, err := url.Parse(r.ServerURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("remote '%s' needs server_url, the full API URL such as http://host:port/team-worker-api", name)
	}
	if r.APIKey == "" {
		return fmt.Errorf("remote '%s' needs api_key", name)
	}
	words := strings.Fields(cmd.CommandPath())[1:]
	for _, prefix := range localOnly {
		if len(words) >= len(prefix) && slices.Equal(words[:len(prefix)], prefix) {
			return fmt.Errorf("%s starts agents on this machine; it is not available with --remote", strings.Join(prefix, " "))
		}
	}
	for _, key := range localIdentity {
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}
	as := r.As
	if as == "" {
		as = "operator"
	}
	set := map[string]string{
		"TEAM_WORKER_SERVER_URL": r.ServerURL, "TEAM_WORKER_API_KEY": r.APIKey,
		"TEAM_WORKER_AGENT_NAME": as, "TEAM_WORKER_NO_AUTOSTART": "1",
	}
	if !r.UseProxy {
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			hosts := strings.FieldsFunc(os.Getenv(key), func(c rune) bool { return c == ',' })
			if !slices.Contains(hosts, u.Hostname()) {
				hosts = append(hosts, u.Hostname())
			}
			set[key] = strings.Join(hosts, ",")
		}
	}
	for key, value := range set {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}
