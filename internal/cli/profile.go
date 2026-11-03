package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/profile"
)

// profileFlags are the fields shared by `profile set` and `preset set-client`.
type profileFlags struct {
	agent, description, command, model, baseURL, baseURLEnv, apiKeyEnv, apiKeyTargetEnv string
	env, envMap, args                                                                   []string
}

func (f *profileFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.agent, "agent", "", "Default agent CLI name (claude/codex/gemini/kimi/…)")
	fl.StringVar(&f.description, "description", "", "Description")
	fl.StringVar(&f.command, "command", "", "Exact command string, split like a shell (e.g. 'kimi --config-file ~/.kimi/config.toml')")
	fl.StringVar(&f.model, "model", "", "Default model, passed as --model when the command has none")
	fl.StringVar(&f.baseURL, "base-url", "", "Provider base URL")
	fl.StringVar(&f.baseURLEnv, "base-url-env", "", "Variable receiving the base URL (default: the agent's usual one)")
	fl.StringVar(&f.apiKeyEnv, "api-key-env", "", "Caller variable holding the API key (the key itself is never stored)")
	fl.StringVar(&f.apiKeyTargetEnv, "api-key-target-env", "", "Variable receiving the API key (default: the agent's usual one)")
	fl.StringArrayVar(&f.env, "env", nil, "Static environment KEY=VALUE (repeatable; replaces the saved set)")
	fl.StringArrayVar(&f.envMap, "env-map", nil, "Copy a caller variable DEST=SOURCE (repeatable; replaces the saved set)")
	fl.StringArrayVar(&f.args, "arg", nil, "Argument appended to the agent command (repeatable; replaces the saved list)")
}

// apply changes only the fields whose flags were given, like ClawTeam.
func (f *profileFlags) apply(cmd *cobra.Command, p profile.Profile) (profile.Profile, error) {
	changed := cmd.Flags().Changed
	if changed("agent") {
		p.Agent = f.agent
	}
	if changed("description") {
		p.Description = f.description
	}
	if changed("command") {
		words, err := splitWords(f.command)
		if err != nil {
			return p, fmt.Errorf("--command: %w", err)
		}
		p.Command = words
	}
	if changed("model") {
		p.Model = f.model
	}
	if changed("base-url") {
		p.BaseURL = f.baseURL
	}
	if changed("base-url-env") {
		p.BaseURLEnv = f.baseURLEnv
	}
	if changed("api-key-env") {
		p.APIKeyEnv = f.apiKeyEnv
	}
	if changed("api-key-target-env") {
		p.APIKeyTargetEnv = f.apiKeyTargetEnv
	}
	var err error
	if len(f.env) > 0 {
		if p.Env, err = keyValues(f.env, "env"); err != nil {
			return p, err
		}
	}
	if len(f.envMap) > 0 {
		if p.EnvMap, err = keyValues(f.envMap, "env-map"); err != nil {
			return p, err
		}
	}
	if len(f.args) > 0 {
		p.Args = append([]string{}, f.args...)
	}
	return p, nil
}

func keyValues(items []string, label string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range items {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --%s %q: use KEY=VALUE", label, kv)
		}
		out[k] = v
	}
	return out, nil
}

// splitWords splits a command string like a POSIX shell, with single and
// double quotes and backslash escapes, without expanding anything.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated double quote")
			}
			inWord = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// The daemon owns the profiles and presets in its config.json, as it
// starts the agents that use them; these commands edit them over the API.
func (a *app) loadProfiles() (profile.Config, error) {
	return a.client().Profiles(context.Background())
}

func (a *app) saveProfiles(c profile.Config) error {
	return a.client().SaveProfiles(context.Background(), c)
}

func (a *app) presetCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "preset", Short: "Endpoint presets that generate client profiles",
		Long: "Presets describe a model endpoint once (base URL, auth variable, environment) with\n" +
			"overrides per client CLI, and generate profiles from them. Built-in presets can be\n" +
			"overridden by local ones of the same name, kept in ~/.team-worker/config.json."}

	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List built-in and local presets", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			list := profile.List(cfg)
			a.emit(list, func(w io.Writer) {
				tw := newTable(w, "NAME", "SOURCE", "CLIENTS", "DESCRIPTION")
				for _, name := range sortedNames(list) {
					l := list[name]
					row(tw, name, l.Source, strings.Join(profile.Clients(l.Preset), ","), l.Preset.Description)
				}
				tw.Flush()
			})
			return nil
		},
	}, &cobra.Command{
		Use: "show NAME", Short: "Show a preset", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			l, err := profile.Find(cfg, args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(l, nil)
			return nil
		},
	})

	var desc, authEnv, baseURL string
	var env []string
	set := &cobra.Command{
		Use: "set NAME", Short: "Create or update a local preset (editing a built-in name overrides it)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			p := profile.Editable(cfg, args[0])
			if cmd.Flags().Changed("description") {
				p.Description = desc
			}
			if cmd.Flags().Changed("auth-env") {
				p.AuthEnv = authEnv
			}
			if cmd.Flags().Changed("base-url") {
				p.BaseURL = baseURL
			}
			if len(env) > 0 {
				if p.Env, err = keyValues(env, "env"); err != nil {
					return a.fail(err)
				}
			}
			cfg.Presets[args[0]] = p
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "saved", "preset": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Saved preset '%s'\n", args[0])
			})
			return nil
		},
	}
	set.Flags().StringVar(&desc, "description", "", "Preset description")
	set.Flags().StringVar(&authEnv, "auth-env", "", "Caller variable holding the provider key")
	set.Flags().StringVar(&baseURL, "base-url", "", "Base URL shared by clients")
	set.Flags().StringArrayVar(&env, "env", nil, "Shared environment KEY=VALUE (repeatable; replaces the saved set)")

	var clientFlags profileFlags
	setClient := &cobra.Command{
		Use: "set-client PRESET CLIENT", Short: "Create or update a client override inside a preset",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			p := profile.Editable(cfg, args[0])
			client := profile.NormalizeClient(args[1])
			override, err := clientFlags.apply(cmd, p.ClientOverrides[client].Clone())
			if err != nil {
				return a.fail(err)
			}
			if len(override.Command) == 0 && override.Agent == "" {
				override.Agent = client
			}
			p.ClientOverrides[client] = override
			cfg.Presets[args[0]] = p
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "saved", "preset": args[0], "client": client}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Saved client override '%s' in preset '%s'\n", client, args[0])
			})
			return nil
		},
	}
	clientFlags.register(setClient)

	cmd.AddCommand(set, setClient, &cobra.Command{
		Use: "copy SOURCE TARGET", Short: "Copy a preset into a new local preset", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			source, err := profile.Find(cfg, args[0])
			if err != nil {
				return a.fail(err)
			}
			if _, exists := profile.List(cfg)[args[1]]; exists {
				return a.fail(fmt.Errorf("Preset '%s' already exists", args[1]))
			}
			cfg.Presets[args[1]] = source.Preset.Clone()
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "copied", "source": args[0], "target": args[1]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Copied preset '%s' to '%s'\n", args[0], args[1])
			})
			return nil
		},
	}, &cobra.Command{
		Use: "remove NAME", Short: "Remove a local preset (built-ins cannot be removed)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			if _, ok := cfg.Presets[args[0]]; !ok {
				return a.fail(fmt.Errorf("Local preset '%s' not found", args[0]))
			}
			delete(cfg.Presets, args[0])
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "removed", "preset": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Removed preset '%s'\n", args[0])
			})
			return nil
		},
	}, &cobra.Command{
		Use: "remove-client PRESET CLIENT", Short: "Remove a client override from a local preset", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			client := profile.NormalizeClient(args[1])
			p, ok := cfg.Presets[args[0]]
			if _, has := p.ClientOverrides[client]; !ok || !has {
				return a.fail(fmt.Errorf("Local preset '%s' has no client override '%s'", args[0], client))
			}
			delete(p.ClientOverrides, client)
			cfg.Presets[args[0]] = p
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "removed", "preset": args[0], "client": client}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Removed client override '%s' from preset '%s'\n", client, args[0])
			})
			return nil
		},
	})

	var genName string
	var genForce bool
	generate := &cobra.Command{
		Use: "generate-profile PRESET CLIENT", Short: "Generate a profile from a preset's client override",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			name, p, err := profile.Generate(cfg, args[0], args[1], genName)
			if err != nil {
				return a.fail(err)
			}
			if _, exists := cfg.Profiles[name]; exists && !genForce {
				return a.fail(fmt.Errorf("Profile '%s' already exists; use --force to replace it", name))
			}
			cfg.Profiles[name] = p
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			client := profile.NormalizeClient(args[1])
			a.emit(map[string]string{"status": "saved", "profile": name, "preset": args[0], "client": client}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Saved profile '%s' from preset '%s' (%s)\n", name, args[0], client)
			})
			return nil
		},
	}
	generate.Flags().StringVar(&genName, "name", "", "Profile name (default: <client>-<preset>)")
	generate.Flags().BoolVar(&genForce, "force", false, "Replace an existing profile")

	var bootClients []string
	var bootForce bool
	bootstrap := &cobra.Command{
		Use: "bootstrap PRESET", Short: "Generate profiles for every client of a preset", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			l, err := profile.Find(cfg, args[0])
			if err != nil {
				return a.fail(err)
			}
			clients := bootClients
			if len(clients) == 0 {
				clients = profile.Clients(l.Preset)
			}
			generated, skipped := []string{}, []string{}
			for _, client := range clients {
				name, p, err := profile.Generate(cfg, args[0], client, "")
				if err != nil {
					return a.fail(err)
				}
				if _, exists := cfg.Profiles[name]; exists && !bootForce {
					skipped = append(skipped, name)
					continue
				}
				cfg.Profiles[name] = p
				generated = append(generated, name)
			}
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"preset": args[0], "generated": generated, "skipped": skipped}, func(w io.Writer) {
				fmt.Fprintf(w, "Preset '%s': generated %s", args[0], strings.Join(generated, ", "))
				if len(generated) == 0 {
					fmt.Fprint(w, "nothing")
				}
				if len(skipped) > 0 {
					fmt.Fprintf(w, "; skipped existing %s", strings.Join(skipped, ", "))
				}
				fmt.Fprintln(w)
			})
			return nil
		},
	}
	bootstrap.Flags().StringArrayVar(&bootClients, "client", nil, "Client to generate (repeatable; default: every client)")
	bootstrap.Flags().BoolVar(&bootForce, "force", false, "Replace existing profiles")
	cmd.AddCommand(generate, bootstrap)
	return cmd
}

func (a *app) profileCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Runtime profiles used by spawn and launch",
		Long: "A profile names an agent command, model, endpoint and credential variables. spawn and\n" +
			"launch use --profile NAME, else default_profile (or TEAM_WORKER_DEFAULT_PROFILE), else the\n" +
			"only profile; an explicit agent command after -- means no implicit profile. Credentials\n" +
			"are environment variable names read from the caller at spawn time, never stored values."}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List profiles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			a.emit(cfg.Profiles, func(w io.Writer) {
				if len(cfg.Profiles) == 0 {
					fmt.Fprintln(w, "No profiles. Create one with `team-worker profile set` or `team-worker preset generate-profile`.")
					return
				}
				tw := newTable(w, "NAME", "AGENT", "MODEL", "DESCRIPTION")
				for _, name := range sortedNames(cfg.Profiles) {
					p := cfg.Profiles[name]
					agentName := p.Agent
					if len(p.Command) > 0 {
						agentName = strings.Join(p.Command, " ")
					}
					if name == cfg.DefaultProfile {
						name += " (default)"
					}
					row(tw, name, agentName, p.Model, p.Description)
				}
				tw.Flush()
			})
			return nil
		},
	}, &cobra.Command{
		Use: "show NAME", Short: "Show a profile", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			p, ok := cfg.Profiles[args[0]]
			if !ok {
				return a.fail(fmt.Errorf("Profile '%s' not found", args[0]))
			}
			a.emit(p, nil)
			return nil
		},
	})

	var flags profileFlags
	set := &cobra.Command{
		Use: "set NAME", Short: "Create or update a profile (only the given flags change)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			p, err := flags.apply(cmd, cfg.Profiles[args[0]].Clone())
			if err != nil {
				return a.fail(err)
			}
			if len(p.Command) == 0 && p.Agent == "" {
				return a.fail(errors.New("Profile must define either --agent or --command."))
			}
			cfg.Profiles[args[0]] = p
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "saved", "profile": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Saved profile '%s'\n", args[0])
			})
			return nil
		},
	}
	flags.register(set)

	var prompt, cwd string
	test := &cobra.Command{
		Use: "test NAME", Short: "Run a profile once with a short prompt and report the result", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.client().TestProfile(cmd.Context(), args[0], prompt, cwd)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Profile:  %s (%s)\nCommand:  %s\nExit:     %d\n", res.Profile, res.Agent,
					strings.Join(res.Command, " "), res.ReturnCode)
				if res.Stdout != "" {
					fmt.Fprintf(w, "\n%s\n", strings.TrimRight(res.Stdout, "\n"))
				}
				if res.Stderr != "" {
					fmt.Fprintf(w, "\nstderr:\n%s\n", strings.TrimRight(res.Stderr, "\n"))
				}
			})
			if res.ReturnCode != 0 {
				return exitError{}
			}
			return nil
		},
	}
	test.Flags().StringVar(&prompt, "prompt", "Reply with exactly TEAM_WORKER_PROFILE_OK", "Prompt sent to the agent")
	test.Flags().StringVar(&cwd, "cwd", "", "Working directory for the test run on the daemon's machine (default: the daemon's)")

	cmd.AddCommand(set, &cobra.Command{
		Use: "remove NAME", Short: "Remove a profile", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadProfiles()
			if err != nil {
				return a.fail(err)
			}
			if _, ok := cfg.Profiles[args[0]]; !ok {
				return a.fail(fmt.Errorf("Unknown profile '%s'", args[0]))
			}
			delete(cfg.Profiles, args[0])
			if cfg.DefaultProfile == args[0] {
				cfg.DefaultProfile = ""
			}
			if err := a.saveProfiles(cfg); err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "removed", "profile": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Removed profile '%s'\n", args[0])
			})
			return nil
		},
	}, test, a.profileWizardCmd(), &cobra.Command{
		Use: "doctor CLIENT", Short: "Repair a client's local state on the daemon's machine (supported: claude)",
		Long: "doctor claude sets hasCompletedOnboarding in ~/.claude.json on the daemon's machine, so\n" +
			"Claude Code pointed at another endpoint starts without its first-run screens. Other keys\n" +
			"of the file are kept; a file that is not valid JSON is left alone and reported.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.client().ProfileDoctor(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) {
				action := "Updated"
				if res.Created {
					action = "Created"
				}
				fmt.Fprintf(w, "OK %s Claude state at '%s' with hasCompletedOnboarding=true\n", action, res.Path)
			})
			return nil
		},
	})
	return cmd
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
