package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

func maskingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "masking",
		Short: "Turn on Magpie privacy masking the first time a home volume starts",
		Long: "Privacy masking (Magpie's redact setting) sends keys, passwords and tokens to\n" +
			"model vendors as placeholders. It is turned on once per home volume; turning it\n" +
			"off in Magpie afterwards sticks. TEAM_WORKER_MASKING=0 skips this step.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv("TEAM_WORKER_MASKING") == "0" {
				return nil
			}
			return failWith("masking setup failed: ", enableMasking(magpieWebBase))
		},
	}
}

// maskingMarker records that masking was turned on once. Magpie omits a false
// redact from its settings, so the setting alone cannot tell "never set" from
// "turned off by the user".
func maskingMarker() string {
	return filepath.Join(configHome(), "team-worker-docker", "masking-initialized")
}

func enableMasking(base string) error {
	marker := maskingMarker()
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	key, err := magpieWebKey()
	if err != nil {
		return err
	}
	lib, err := newLibrary(base, key)
	if err != nil {
		return err
	}
	settings, err := lib.View("/api/settings", nil)
	if err != nil {
		return err
	}
	if settings["redact"] != true {
		// The Magpie page saves the settings it read with its change applied;
		// do the same so other settings are kept.
		settings["redact"] = true
		saved, err := lib.View("/api/settings", settings)
		if err != nil {
			return err
		}
		if saved["redact"] != true {
			return errors.New("Magpie did not keep privacy masking on")
		}
		fmt.Println("masking: turned on Magpie privacy masking")
	}
	return writeAtomic(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}
