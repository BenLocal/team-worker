package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// providerAPIs maps providers.yaml api values to Magpie provider options.
var providerAPIs = map[string]string{
	"openai":    "url",
	"anthropic": "anthropic",
	"responses": "responses",
	"gemini":    "gemini",
}

// modelAPINames maps providers.yaml api values to the API names Magpie's
// modelAPIs setting takes.
var modelAPINames = map[string]string{
	"openai":    "chat",
	"anthropic": "anthropic",
	"responses": "responses",
	"gemini":    "gemini",
}

// pendingPath marks providers waiting for an API key; the leader supervisor waits on it.
var pendingPath = "/tmp/team-worker-provider-pending"

func magpieCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "magpie PROVIDERS_FILE",
		Short: "Apply providers.yaml to Magpie and print the Claude model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model, err := configureMagpie(args[0], environMap(), runMagpie)
			if errors.Is(err, errYAML) {
				// YAML parse errors quote their input, which may contain a provider key.
				return exitError{code: 1, msg: "Magpie setup failed; check the format, models and credential environment variables in providers.yaml."}
			}
			if err != nil {
				return failWith("Magpie setup failed: ", err)
			}
			if model != "" {
				fmt.Println(model)
			}
			return nil
		},
	}
}

// runMagpie runs the magpie CLI without echoing its output, which can contain
// the API key from its arguments or the upstream URL.
func runMagpie(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "magpie", args...).Run()
	if ctx.Err() != nil {
		return fmt.Errorf("magpie %s configuration timed out", args[0])
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("magpie %s configuration failed with exit code %d; check providers.yaml", args[0], exit.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("cannot run magpie: %w", err)
	}
	return nil
}

type magpieConfig struct {
	providers []map[string]any
	groups    []map[string]any
	claude    map[string]any
	codex     map[string]any
}

// entryList converts a list (or an absent value) to a list of objects.
func entryList(v any) ([]map[string]any, bool) {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := asMap(item)
		if !ok {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// stringOr returns m[key] as a string, def when absent, and false when the
// value is present but not a string.
func stringOr(m map[string]any, key, def string) (string, bool) {
	v, present := m[key]
	if !present {
		return def, true
	}
	s, ok := v.(string)
	return s, ok
}

// validateProviders checks the whole providers.yaml before anything changes.
func validateProviders(raw any, env map[string]string) (magpieConfig, error) {
	var c magpieConfig
	config, ok := asMap(raw)
	if !ok {
		return c, errors.New("providers.yaml must be a YAML object")
	}
	if !keysWithin(config, "providers", "groups", "claude", "codex") {
		return c, errors.New("providers.yaml contains unknown top-level fields")
	}
	for _, key := range []string{"providers", "groups"} {
		if v, present := config[key]; present && !isList(v) {
			return c, errors.New("providers and groups must be lists")
		}
	}
	if c.providers, ok = entryList(config["providers"]); ok {
		c.groups, ok = entryList(config["groups"])
	}
	if !ok {
		return c, errors.New("malformed provider or group, or unknown fields")
	}
	for _, set := range []struct {
		entries []map[string]any
		fields  []string
	}{
		{c.providers, []string{"id", "api", "url", "url_env", "urls", "model_api", "key", "key_env", "key_required", "models"}},
		{c.groups, []string{"id", "models", "routing", "stays"}},
	} {
		seen := map[string]bool{}
		for _, entry := range set.entries {
			if !keysWithin(entry, set.fields...) {
				return c, errors.New("malformed provider or group, or unknown fields")
			}
			id, isString := stringOr(entry, "id", "")
			if !isString || !providerID.MatchString(id) || seen[id] {
				return c, errors.New("provider/group ids must be unique and contain only lowercase letters, digits and hyphens")
			}
			seen[id] = true
			models, ok := entry["models"].([]any)
			valid := ok && len(models) > 0
			for _, m := range models {
				s, isString := m.(string)
				if !isString || strings.TrimSpace(s) == "" || strings.Contains(s, ",") {
					valid = false
				}
			}
			if !valid {
				return c, fmt.Errorf("%s needs a non-empty models list", id)
			}
		}
	}
	for _, entry := range c.providers {
		id := entry["id"].(string)
		_, hasURL := entry["url"]
		_, hasURLEnv := entry["url_env"]
		if hasURL == hasURLEnv {
			return c, errors.New("a provider must set exactly one of url and url_env")
		}
		var rawURL any = entry["url"]
		if hasURLEnv {
			name, isString := entry["url_env"].(string)
			value, set := env[name]
			if !isString || !set {
				return c, fmt.Errorf("provider %s references an unset url_env", id)
			}
			rawURL = value
		}
		if s, isString := rawURL.(string); !isString || !validURL(s) {
			return c, errors.New("provider url must be an HTTP(S) address without credentials")
		}
		api, isString := stringOr(entry, "api", "openai")
		if _, known := providerAPIs[api]; !isString || !known {
			return c, errors.New("provider api must be one of openai, anthropic, responses, gemini")
		}
		// urls adds the provider's other APIs; the api's own is url.
		apis := map[string]bool{api: true}
		if v, present := entry["urls"]; present {
			urls, isMap := asMap(v)
			if !isMap {
				return c, fmt.Errorf("provider %s urls must map an api to a URL", id)
			}
			for other, u := range urls {
				s, isString := u.(string)
				if _, known := providerAPIs[other]; !known || other == api || !isString || !validURL(s) {
					return c, fmt.Errorf("provider %s urls must map other apis (openai, anthropic, responses, gemini) to HTTP(S) addresses without credentials", id)
				}
				apis[other] = true
			}
		}
		if v, present := entry["model_api"]; present {
			s, isString := v.(string)
			if !isString || !apis[s] {
				return c, fmt.Errorf("provider %s model_api must be an api it has a URL for (api or urls)", id)
			}
		}
		_, hasKey := entry["key"]
		_, hasKeyEnv := entry["key_env"]
		if hasKey && hasKeyEnv {
			return c, errors.New("set only one of key and key_env")
		}
		if hasKeyEnv {
			name, isString := entry["key_env"].(string)
			if _, set := env[name]; !isString || !set {
				return c, fmt.Errorf("provider %s references an unset key_env", id)
			}
		} else if _, isString := stringOr(entry, "key", ""); !isString {
			return c, errors.New("provider key must be a string; use an empty string for no authentication")
		}
		if v, present := entry["key_required"]; present {
			if _, isBool := v.(bool); !isBool {
				return c, errors.New("key_required must be a boolean")
			}
		}
	}
	routings := map[string]bool{"smart": true, "order": true, "rotate": true, "usage": true, "pace": true, "weight": true, "free": true, "fast": true}
	stays := map[string]bool{"auto": true, "session": true, "turn": true, "off": true, "strict": true}
	for _, entry := range c.groups {
		if r, isString := stringOr(entry, "routing", "order"); !isString || !routings[r] {
			return c, errors.New("unsupported group routing")
		}
		if s, isString := stringOr(entry, "stays", "off"); !isString || !stays[s] {
			return c, errors.New(`group stays must be a string, e.g. stays: "off"`)
		}
	}
	c.claude = map[string]any{}
	if v, present := config["claude"]; present {
		m, isMap := asMap(v)
		if !isMap || !keysWithin(m, "model", "effort") {
			return c, errors.New("claude supports only the model and effort fields")
		}
		c.claude = m
	}
	if _, isString := stringOr(c.claude, "model", ""); !isString {
		return c, errors.New("claude.model must be a string")
	}
	if v, present := c.claude["effort"]; present {
		efforts := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		if s, isString := v.(string); !isString || !efforts[s] {
			return c, errors.New("unsupported claude.effort")
		}
	}
	c.codex = map[string]any{}
	if v, present := config["codex"]; present {
		m, isMap := asMap(v)
		if !isMap || !keysWithin(m, "model", "effort") {
			return c, errors.New("codex supports only the model and effort fields")
		}
		c.codex = m
	}
	if _, isString := stringOr(c.codex, "model", ""); !isString {
		return c, errors.New("codex.model must be a string")
	}
	if v, present := c.codex["effort"]; present {
		efforts := map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}
		if s, isString := v.(string); !isString || !efforts[s] {
			return c, errors.New("unsupported codex.effort")
		}
	}
	return c, nil
}

// validURL reports whether s is an HTTP(S) address without credentials.
func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func isList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// configureMagpie applies providers and groups through the magpie CLI and
// returns the Claude model it selected, or "" when none is configured.
func configureMagpie(path string, env map[string]string, magpie func(...string) error) (string, error) {
	raw, err := loadYAML(path)
	if err != nil {
		return "", err
	}
	c, err := validateProviders(raw, env)
	if err != nil {
		return "", err
	}
	saved, err := readJSONFile(filepath.Join(configHome(), "magpie/providers.json"))
	if err != nil {
		return "", err
	}
	existing := agentIDs(saved["providers"], "id")
	var pending []string
	for _, entry := range c.providers {
		id := entry["id"].(string)
		var u string
		if name, ok := entry["url_env"].(string); ok {
			u = env[name]
		} else {
			u = entry["url"].(string)
		}
		var key string
		if name, ok := entry["key_env"].(string); ok {
			key = env[name]
		} else {
			key, _ = stringOr(entry, "key", "")
		}
		if required, _ := entry["key_required"].(bool); required && strings.TrimSpace(key) == "" {
			pending = append(pending, id)
			// Some vendors refuse an empty key even when only saving config.
			// The leader never uses this placeholder: its supervisor waits.
			key = "team-worker-unconfigured"
		}
		api, _ := stringOr(entry, "api", "openai")
		options := []string{
			providerAPIs[api] + "=" + u,
			"key=" + key,
			"models=" + joinModels(entry["models"]),
		}
		urls, _ := asMap(entry["urls"])
		for _, other := range slices.Sorted(maps.Keys(urls)) {
			options = append(options, providerAPIs[other]+"="+urls[other].(string))
		}
		if existing[id] {
			err = magpie(append([]string{"provider", "set", id}, options...)...)
		} else {
			err = magpie(append([]string{"provider", "add", id, "id=" + id}, options...)...)
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "configured Magpie provider: %s\n", id)
	}
	if err := setModelAPIs(c.providers); err != nil {
		return "", err
	}
	for _, entry := range c.groups {
		routing, _ := stringOr(entry, "routing", "order")
		stays, _ := stringOr(entry, "stays", "off")
		id := entry["id"].(string)
		if err := magpie("group", "add", id, "models="+joinModels(entry["models"]), "routing="+routing, "stays="+stays); err != nil {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "configured Magpie group: %s\n", id)
	}
	model := env["TEAM_WORKER_MODEL"]
	if model == "" {
		model, _ = stringOr(c.claude, "model", "")
	}
	if model == "" {
		if model, err = magpieClaudeModel(); err != nil {
			return "", err
		}
	}
	// A model Magpie cannot apply now, e.g. on a provider turned off in its
	// panel or out of quota, only warns: failing here would stop the
	// container and restart it into the same failure. The agents keep the
	// models Magpie already gives them.
	if model != "" {
		if err := magpie("claude", model); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot set Claude's model to %s (%v); Claude keeps its current model\n", model, err)
			if model, err = magpieClaudeModel(); err != nil {
				return "", err
			}
		} else if effort, ok := c.claude["effort"].(string); ok {
			if err := magpie("claude", "effort", effort); err != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot set Claude's effort to %s (%v)\n", effort, err)
			}
		}
	}
	if model != "" {
		if _, err := exec.LookPath("dsh"); err == nil {
			// dsh workers share the leader's model; Magpie writes every dsh profile.
			if err := magpie("dsh", model); err != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot set dsh's model to %s (%v)\n", model, err)
			}
		}
		if err := trustWorkspace(); err != nil {
			return "", err
		}
	}
	// Codex workers use codex.model, or else the leader's model like dsh.
	codexModel, _ := stringOr(c.codex, "model", "")
	if codexModel == "" {
		codexModel = model
	}
	if _, err := exec.LookPath("codex"); err == nil && codexModel != "" {
		if err := magpie("codex", codexModel); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot set Codex's model to %s (%v); Codex keeps its current model\n", codexModel, err)
		} else if effort, ok := c.codex["effort"].(string); ok {
			if err := magpie("codex", "effort", effort); err != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot set Codex's effort to %s (%v)\n", effort, err)
			}
		}
	}
	if len(pending) > 0 {
		if err := os.WriteFile(pendingPath, []byte(strings.Join(pending, ", ")), 0o644); err != nil {
			return "", err
		}
		fmt.Fprintln(os.Stderr, "waiting for API keys: "+strings.Join(pending, ", "))
	} else if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return model, nil
}

// setModelAPIs writes each provider's model_api into Magpie's modelAPIs
// setting, which says the API a model is asked on; the gateway reads it on
// every request. The file owns the providers it lists: their earlier entries
// are replaced, so taking model_api out of the file takes the setting out of
// Magpie. Magpie 0.1.2925 has no command for this setting, so its
// settings.json is edited.
func setModelAPIs(providers []map[string]any) error {
	path := filepath.Join(configHome(), "magpie/settings.json")
	settings, err := readJSONFile(path)
	if err != nil {
		return err
	}
	current, _ := asMap(settings["modelAPIs"])
	next := map[string]any{}
	for ref, api := range current {
		next[ref] = api
	}
	for _, entry := range providers {
		id := entry["id"].(string)
		for ref := range next {
			if strings.HasPrefix(ref, id+"/") {
				delete(next, ref)
			}
		}
		if api, ok := entry["model_api"].(string); ok {
			for _, m := range entry["models"].([]any) {
				next[id+"/"+m.(string)] = modelAPINames[api]
			}
		}
	}
	if maps.Equal(current, next) {
		return nil
	}
	if len(next) == 0 {
		delete(settings, "modelAPIs")
	} else {
		settings["modelAPIs"] = next
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(path, append(data, '\n'), mode); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "configured Magpie model APIs")
	return nil
}

// magpieClaudeModel returns the model Magpie gives Claude now, or "" when
// Claude does not go through the Magpie gateway.
func magpieClaudeModel() (string, error) {
	settings, err := readJSONFile(filepath.Join(homeDir(), ".claude/settings.json"))
	if err != nil {
		return "", err
	}
	settingsEnv, _ := asMap(settings["env"])
	base, _ := settingsEnv["ANTHROPIC_BASE_URL"].(string)
	if !strings.HasPrefix(base, "http://127.0.0.1:3425") {
		return "", nil
	}
	model, _ := settings["model"].(string)
	return model, nil
}

func joinModels(v any) string {
	items, _ := v.([]any)
	models := make([]string, len(items))
	for i, item := range items {
		models[i] = item.(string)
	}
	return strings.Join(models, ",")
}

// trustWorkspace lets an explicitly mounted Git workspace start on a
// configured API model without the first-run sign-in or trust screens.
func trustWorkspace() error {
	path := filepath.Join(homeDir(), ".claude.json")
	state, err := readJSONFile(path)
	if err != nil {
		return err
	}
	state["hasCompletedOnboarding"] = true
	projects, ok := asMap(state["projects"])
	if !ok {
		projects = map[string]any{}
		state["projects"] = projects
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project, ok := asMap(projects[cwd])
	if !ok {
		project = map[string]any{}
		projects[cwd] = project
	}
	project["hasTrustDialogAccepted"] = true
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
