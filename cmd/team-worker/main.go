// Command team-worker is the CLI that agents and people use to coordinate a team.
package main

import (
	"os"

	"github.com/benlocal/team-worker/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
