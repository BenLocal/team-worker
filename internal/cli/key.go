package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func (a *app) keyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Manage the shared Web and HTTP API access key"}
	cmd.AddCommand(&cobra.Command{Use: "show", Short: "Read the access key through the local Unix socket", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		key, err := a.client().AccessKey(cmd.Context())
		if err != nil {
			return a.fail(err)
		}
		a.emit(map[string]string{"key": key}, func(w io.Writer) { fmt.Fprintln(w, key) })
		return nil
	}})
	return cmd
}
