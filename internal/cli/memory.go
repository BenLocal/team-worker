package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) memoryCmd() *cobra.Command {
	var user string
	root := &cobra.Command{Use: "memory", Short: "Manage a member's persistent Markdown documents and reflection"}
	root.PersistentFlags().StringVar(&user, "user", config.IdentityFromEnv().User, "Member's user")
	specs := []struct {
		action, use, help string
		min, max          int
	}{
		{"init", "init TEAM NAME", "Initialize missing personal documents", 2, 2},
		{"list", "list TEAM NAME", "List documents and their content hashes", 2, 2},
		{"read", "read TEAM NAME PATH", "Read a personal document", 3, 3},
		{"write", "write TEAM NAME PATH [TEXT]", "Write from TEXT, --file or stdin with a base hash", 3, 4},
		{"search", "search TEAM NAME QUERY", "Search document names and contents", 3, 3},
		{"history", "history TEAM NAME PATH", "Show retained versions of a document", 3, 3},
		{"rollback", "rollback TEAM NAME PATH REVISION", "Restore a historical version with a base hash", 4, 4},
		{"context", "context TEAM NAME", "Load the current personal context", 2, 2},
		{"ack", "ack TEAM NAME", "Acknowledge the current file hashes after reading them", 2, 2},
		{"status", "status TEAM NAME", "Show current, provided and acknowledged document versions", 2, 2},
		{"reflect-config", "reflect-config TEAM NAME [JSON]", "Read reflection settings or replace them from JSON", 2, 3},
		{"reflect", "reflect TEAM NAME", "Generate evidence-based candidate edits", 2, 2},
		{"reflect-status", "reflect-status TEAM NAME", "Show the most recent reflection", 2, 2},
		{"reflect-discard", "reflect-discard TEAM NAME REVISION", "Discard a candidate without changing documents", 3, 3},
		{"reflect-apply", "reflect-apply TEAM NAME REVISION", "Apply a candidate while the member is stopped", 3, 3},
	}
	for _, spec := range specs {
		var file, base, hashes, run string
		var dry bool
		cmd := &cobra.Command{Use: spec.use, Short: spec.help, Args: cobra.RangeArgs(spec.min, spec.max)}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			in := model.MemoryRequest{BaseHash: base, RunID: run, DryRun: dry}
			if spec.action == "read" || spec.action == "write" || spec.action == "history" || spec.action == "rollback" {
				in.Path = args[2]
			}
			if spec.action == "search" {
				in.Query = args[2]
			}
			if spec.action == "rollback" {
				in.Revision = args[3]
			}
			if spec.action == "reflect-apply" || spec.action == "reflect-discard" {
				in.Revision = args[2]
			}
			if spec.action == "write" || spec.action == "rollback" {
				if !cmd.Flags().Changed("base-hash") {
					return a.fail(fmt.Errorf("--base-hash is required; use an empty hash only to create a file"))
				}
			}
			if hashes != "" {
				if err := json.Unmarshal([]byte(hashes), &in.Hashes); err != nil {
					return a.fail(err)
				}
			}
			if spec.action == "write" || spec.action == "reflect-config" && (len(args) == 3 || cmd.Flags().Changed("file")) {
				var data []byte
				var err error
				switch {
				case spec.action == "write" && len(args) == 4:
					data = []byte(args[3])
				case spec.action == "reflect-config" && len(args) == 3:
					data = []byte(args[2])
				case cmd.Flags().Changed("file"):
					data, err = os.ReadFile(file)
				default:
					data, err = io.ReadAll(io.LimitReader(a.in, (256<<10)+1))
				}
				if err != nil {
					return a.fail(err)
				}
				if spec.action == "write" {
					in.Content = string(data)
				} else {
					in.Config = &model.ReflectionConfig{}
					if err = json.Unmarshal(data, in.Config); err != nil {
						return a.fail(err)
					}
				}
			}
			out, err := a.client().Memory(cmd.Context(), model.MemoryIdentity{Team: args[0], Name: args[1], User: user}, spec.action, in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(out, func(w io.Writer) {
				switch {
				case spec.action == "read":
					for _, d := range out.Documents {
						fmt.Fprint(w, d.Content)
					}
				case spec.action == "list":
					for _, d := range out.Documents {
						fmt.Fprintf(w, "%s  %s\n", d.Hash, d.Path)
					}
				case out.Context != "":
					fmt.Fprint(w, out.Context)
				default:
					b, e := json.MarshalIndent(out, "", "  ")
					if e == nil {
						fmt.Fprintln(w, string(b))
					}
				}
			})
			return nil
		}
		cmd.Flags().StringVar(&file, "file", "", "Read text or configuration JSON from a local file")
		cmd.Flags().StringVar(&base, "base-hash", "", "Expected current hash; empty only for a new file")
		cmd.Flags().StringVar(&hashes, "hashes", "", "JSON object of current path-to-hash pairs")
		cmd.Flags().StringVar(&run, "run-id", os.Getenv("TEAM_WORKER_RUN_ID"), "Current agent run ID")
		cmd.Flags().BoolVar(&dry, "dry-run", false, "Preview context without creating or updating files")
		root.AddCommand(cmd)
	}
	return root
}
