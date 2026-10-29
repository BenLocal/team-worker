package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// promptCmd imports the structured prompt.yaml into Magpie Instructions
// before agents start.
func promptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prompt FILE",
		Short: "Import prompt.yaml into Magpie Instructions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := configurePrompt(magpieWebBase, args[0], cmd.OutOrStdout())
			return failWith("prompt setup failed: ", err)
		},
	}
}

func readInstructions(path string) (map[string]any, error) {
	v, err := loadYAML(path)
	if err != nil {
		return nil, err
	}
	config, ok := asMap(v)
	if !ok || !keysExactly(config, "instructions") {
		return nil, errors.New("prompt.yaml must contain only an instructions object")
	}
	instructions, ok := asMap(config["instructions"])
	if !ok {
		return nil, errors.New("instructions must be an object")
	}
	if !keysWithin(instructions, "shared", "agents", "extra") {
		return nil, errors.New("instructions only supports shared, agents and extra")
	}
	if v, ok := instructions["shared"]; ok {
		if _, ok := v.(string); !ok {
			return nil, errors.New("instructions.shared must be a string")
		}
	}
	if v, ok := instructions["agents"]; ok {
		if _, ok := stringList(v); !ok {
			return nil, errors.New("instructions.agents must be a list of strings")
		}
	}
	if v, ok := instructions["extra"]; ok {
		extra, ok := asMap(v)
		if !ok {
			return nil, errors.New("instructions.extra must map agents to text")
		}
		for _, text := range extra {
			if _, ok := text.(string); !ok {
				return nil, errors.New("instructions.extra must map agents to text")
			}
		}
	}
	return instructions, nil
}

func configurePrompt(base, path string, out io.Writer) error {
	instructions, err := readInstructions(path)
	if err != nil || len(instructions) == 0 {
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
	view, err := lib.View("/api/library", nil)
	if err != nil {
		return err
	}
	current, _ := asMap(view["instructions"])
	known := agentIDs(current["agents"], "agent")
	requested, _ := stringList(instructions["agents"])
	extra, _ := asMap(instructions["extra"])
	for agent := range extra {
		requested = append(requested, agent)
	}
	for _, agent := range requested {
		if !known[agent] {
			return errors.New("prompt.yaml names agents that Magpie Instructions does not know")
		}
	}
	saved, err := lib.View("/api/library/instructions/save", instructions)
	if err != nil {
		return err
	}
	result, ok := asMap(saved["result"])
	if !ok {
		return errors.New("Magpie Instructions returned no save result")
	}
	if truthy(result["problems"]) {
		return errors.New("Magpie Instructions sync failed; check the agent instruction files")
	}
	fmt.Fprintln(out, "prompt: imported Magpie Instructions.")
	return nil
}
