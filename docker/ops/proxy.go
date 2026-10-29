package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func proxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "proxy",
		Short: "Pass runtime proxies to inner Docker containers and builds, preserving auth",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := configureDockerProxy(environMap()); err != nil {
				return exitError{code: 1, msg: "Docker proxy setup failed; check the format and permissions of ~/.docker/config.json."}
			}
			return nil
		},
	}
}

// environMap returns the process environment as a map.
func environMap() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		env[name] = value
	}
	return env
}

// dockerEnvironment normalizes Docker proxies without changing the
// application's environment: MIRROR_HTTP_PROXY wins for HTTP(S), both
// spellings are set, and loopback addresses are always bypassed.
func dockerEnvironment(env map[string]string) map[string]string {
	result := map[string]string{}
	mirror := env["MIRROR_HTTP_PROXY"]
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		value := env[name]
		if value == "" {
			value = env[strings.ToLower(name)]
		}
		if mirror != "" && name != "ALL_PROXY" {
			value = mirror
		}
		if value != "" {
			result[name] = value
			result[strings.ToLower(name)] = value
		}
	}
	raw := env["NO_PROXY"]
	if raw == "" {
		raw = env["no_proxy"]
	}
	var bypass []string
	for _, item := range strings.Split(raw, ",") {
		if item != "" {
			bypass = append(bypass, item)
		}
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		found := false
		for _, item := range bypass {
			if item == host {
				found = true
				break
			}
		}
		if !found {
			bypass = append(bypass, host)
		}
	}
	result["NO_PROXY"] = strings.Join(bypass, ",")
	result["no_proxy"] = result["NO_PROXY"]
	return result
}

// configureDockerProxy writes the normalized proxies into the default entry
// of Docker's config.json, keeping registry logins and other daemons' entries.
func configureDockerProxy(env map[string]string) error {
	dir := env["DOCKER_CONFIG"]
	if dir == "" {
		dir = filepath.Join(homeDir(), ".docker")
	}
	path := filepath.Join(dir, "config.json")
	config := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		m, ok := asMap(v)
		if !ok {
			return errors.New("docker config must be an object")
		}
		config = m
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if config["proxies"] == nil {
		config["proxies"] = map[string]any{}
	}
	proxies, ok := asMap(config["proxies"])
	if !ok {
		return errors.New("docker proxies must be an object")
	}
	if proxies["default"] == nil {
		proxies["default"] = map[string]any{}
	}
	defaults, ok := asMap(proxies["default"])
	if !ok {
		return errors.New("docker default proxy must be an object")
	}
	normalized := dockerEnvironment(env)
	for _, pair := range [][2]string{
		{"HTTP_PROXY", "httpProxy"},
		{"HTTPS_PROXY", "httpsProxy"},
		{"ALL_PROXY", "allProxy"},
		{"NO_PROXY", "noProxy"},
	} {
		if value := normalized[pair[0]]; value != "" {
			defaults[pair[1]] = value
		} else {
			delete(defaults, pair[1])
		}
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(out, '\n'), 0o600)
}
