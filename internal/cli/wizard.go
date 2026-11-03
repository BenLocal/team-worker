package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/profile"
)

// errCancelled ends the wizard when its input runs out.
var errCancelled = errors.New("Cancelled.")

// asker asks questions one line at a time: the prompt goes to w (stderr,
// so --json output stays clean) and the answer comes from r.
type asker struct {
	r *bufio.Reader
	w io.Writer
}

// line returns the trimmed answer, or def when the answer is empty.
func (q *asker) line(prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(q.w, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(q.w, "%s: ", prompt)
	}
	s, err := q.r.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		fmt.Fprintln(q.w)
		return "", errCancelled
	}
	if s = strings.TrimSpace(s); s == "" {
		return def, nil
	}
	return s, nil
}

func (q *asker) confirm(prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		s, err := q.line(prompt+" ("+hint+")", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(q.w, "Answer y or n.")
	}
}

// choice is one option of a numbered menu.
type choice struct{ label, value string }

// choose prints a numbered menu and returns the chosen value; Enter takes
// the first option.
func (q *asker) choose(prompt string, choices []choice) (string, error) {
	fmt.Fprintln(q.w, prompt)
	for i, c := range choices {
		fmt.Fprintf(q.w, "  %d) %s\n", i+1, c.label)
	}
	for {
		s, err := q.line("Choose", "1")
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(choices) {
			return choices[n-1].value, nil
		}
		for _, c := range choices {
			if s == c.value {
				return c.value, nil
			}
		}
		fmt.Fprintf(q.w, "Enter a number from 1 to %d.\n", len(choices))
	}
}

// words asks for a shell-split list such as a command or arguments.
func (q *asker) words(prompt string, def []string) ([]string, error) {
	for {
		s, err := q.line(prompt, strings.Join(def, " "))
		if err != nil {
			return nil, err
		}
		if s == "-" {
			return nil, nil
		}
		w, err := splitWords(s)
		if err == nil {
			return w, nil
		}
		fmt.Fprintf(q.w, "%v\n", err)
	}
}

// pairs asks for KEY=VALUE entries until an empty line, starting from cur.
func (q *asker) pairs(prompt string, cur map[string]string) (map[string]string, error) {
	out := maps.Clone(cur)
	if out == nil {
		out = map[string]string{}
	}
	for {
		s, err := q.line(prompt+" (KEY=VALUE, empty line to finish)", "")
		if err != nil {
			return nil, err
		}
		if s == "" {
			return out, nil
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			fmt.Fprintln(q.w, "Use KEY=VALUE.")
			continue
		}
		out[k] = v
	}
}

func (a *app) profileWizardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wizard",
		Short: "Create a profile interactively from a preset or by hand",
		Long: "Asks for a client, a preset (or a custom endpoint), a name and the settings to change, shows\n" +
			"the result and saves it on the daemon. It can then run `profile doctor claude` and\n" +
			"`profile test`. Menus take a number; Enter keeps the value in brackets; `-` clears a list.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := a.profileWizard(cmd)
			if errors.Is(err, errCancelled) {
				return a.fail(err)
			}
			return err
		},
	}
}

func (a *app) profileWizard(cmd *cobra.Command) error {
	q := &asker{r: bufio.NewReader(a.in), w: a.errOut}
	cfg, err := a.loadProfiles()
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(q.w, "team-worker profile wizard")
	mode, err := q.choose("Setup mode", []choice{{"Quick setup", "quick"}, {"Advanced setup", "advanced"}})
	if err != nil {
		return err
	}
	var clients []choice
	for _, d := range builtin.Defs() {
		clients = append(clients, choice{d.Name, d.Name})
	}
	client, err := q.choose("Client", clients)
	if err != nil {
		return err
	}

	listed := profile.List(cfg)
	var providers []choice
	for _, name := range slices.Sorted(maps.Keys(listed)) {
		l := listed[name]
		if _, ok := l.Preset.ClientOverrides[client]; !ok {
			continue
		}
		label := fmt.Sprintf("%s  [%s]", name, l.Source)
		if l.Preset.Description != "" {
			label += "  " + l.Preset.Description
		}
		providers = append(providers, choice{label, name})
	}
	providers = append(providers, choice{"Custom endpoint / manual configuration", ""})
	provider, err := q.choose("Provider", providers)
	if err != nil {
		return err
	}

	suggested := client + "-custom"
	p := profile.Profile{Agent: client, Description: "Custom " + client + " profile"}
	if provider != "" {
		if suggested, p, err = profile.Generate(cfg, provider, client, ""); err != nil {
			return a.fail(err)
		}
	}
	name, err := q.line("Profile name", suggested)
	if err != nil {
		return err
	}

	edit := mode == "advanced" || provider == ""
	if !edit {
		fmt.Fprintf(q.w, "Using the recommended settings of preset '%s'.\n", provider)
		if edit, err = q.confirm("Edit model, endpoint and auth settings?", false); err != nil {
			return err
		}
	}
	if edit {
		for _, f := range []struct {
			prompt string
			v      *string
		}{
			{"Description", &p.Description},
			{"Default model", &p.Model},
			{"Base URL", &p.BaseURL},
			{"API key env var name", &p.APIKeyEnv},
		} {
			if *f.v, err = q.line(f.prompt, *f.v); err != nil {
				return err
			}
		}
	}

	advanced := mode == "advanced"
	if !advanced {
		if advanced, err = q.confirm("Open advanced options (command, args, env)?", false); err != nil {
			return err
		}
	}
	if advanced {
		if p.Agent, err = q.line("Agent CLI name", p.Agent); err != nil {
			return err
		}
		if p.Command, err = q.words("Exact command (optional, `-` for none)", p.Command); err != nil {
			return err
		}
		if p.Args, err = q.words("Extra args (optional, `-` for none)", p.Args); err != nil {
			return err
		}
		if p.Env, err = q.pairs("Static env", p.Env); err != nil {
			return err
		}
		if p.EnvMap, err = q.pairs("Env map from a caller variable, DEST=SOURCE", p.EnvMap); err != nil {
			return err
		}
	}
	if len(p.Command) == 0 && p.Agent == "" {
		return a.fail(errors.New("Profile must define either an agent CLI name or a command."))
	}

	printProfilePreview(q.w, name, p)
	if _, exists := cfg.Profiles[name]; exists {
		ok, err := q.confirm(fmt.Sprintf("Profile '%s' already exists. Overwrite it?", name), false)
		if err != nil {
			return err
		}
		if !ok {
			return a.fail(errors.New("Wizard cancelled without saving."))
		}
	}
	cfg.Profiles[name] = p
	if err := a.saveProfiles(cfg); err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(q.w, "OK Saved profile '%s'\n", name)
	result := map[string]any{"status": "saved", "profile": name}

	agentName := profile.NormalizeClient(p.Agent)
	if agentName == "" {
		agentName = profile.NormalizeClient(profile.Basename(p.Command))
	}
	if agentName == "claude" {
		ok, err := q.confirm("Run `profile doctor claude` now to skip Claude Code's first-run screens?", true)
		if err != nil {
			return err
		}
		if ok {
			res, err := a.client().ProfileDoctor(cmd.Context(), "claude")
			if err != nil {
				return a.fail(err)
			}
			fmt.Fprintf(q.w, "OK Updated %s with hasCompletedOnboarding=true\n", res.Path)
			result["doctor"] = res
		}
	}
	test, err := q.confirm("Run a smoke test for this profile now?", false)
	if err != nil {
		return err
	}
	if test {
		cwd, err := q.line("Working directory on the daemon's machine (optional)", "")
		if err != nil {
			return err
		}
		res, err := a.client().TestProfile(cmd.Context(), name, "Reply with exactly TEAM_WORKER_PROFILE_OK", cwd)
		if err != nil {
			return a.fail(err)
		}
		result["test"] = res
		a.emit(result, func(w io.Writer) {
			fmt.Fprintf(w, "Test exit %d\n%s", res.ReturnCode, res.Stdout)
			if res.Stderr != "" {
				fmt.Fprintf(w, "stderr:\n%s", res.Stderr)
			}
		})
		if res.ReturnCode != 0 {
			return exitError{}
		}
		return nil
	}
	a.emit(result, func(io.Writer) {})
	return nil
}

func printProfilePreview(w io.Writer, name string, p profile.Profile) {
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	fmt.Fprintln(w, "\nProfile preview")
	fmt.Fprintf(w, "  Name:        %s\n", name)
	fmt.Fprintf(w, "  Agent:       %s\n", or(p.Agent, "(unset)"))
	fmt.Fprintf(w, "  Command:     %s\n", or(strings.Join(p.Command, " "), "(derived from agent)"))
	fmt.Fprintf(w, "  Model:       %s\n", or(p.Model, "(default)"))
	fmt.Fprintf(w, "  Base URL:    %s\n", or(p.BaseURL, "(default)"))
	fmt.Fprintf(w, "  API key env: %s\n", or(p.APIKeyEnv, "(unset)"))
	if len(p.Args) > 0 {
		fmt.Fprintf(w, "  Extra args:  %s\n", strings.Join(p.Args, " "))
	}
	for _, k := range slices.Sorted(maps.Keys(p.Env)) {
		fmt.Fprintf(w, "  Env:         %s=%s\n", k, p.Env[k])
	}
	for _, k := range slices.Sorted(maps.Keys(p.EnvMap)) {
		fmt.Fprintf(w, "  Env map:     %s <- $%s\n", k, p.EnvMap[k])
	}
	fmt.Fprintln(w)
}
