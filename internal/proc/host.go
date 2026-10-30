package proc

import (
	"os"

	"github.com/benlocal/team-worker/internal/model"
)

// Host names this machine as recorded with the agents it starts.
func Host() string {
	h, _ := os.Hostname()
	return h
}

// OnServer reports whether the daemon, running here, can reach a's process.
// Agents without a host were recorded on the server's machine.
func OnServer(a model.Agent) bool {
	return a.Host == "" || a.Host == Host()
}

// OnClient reports whether a CLI running here can reach a's process. A remote
// CLI talks to a server elsewhere, so an empty host is not this machine.
func OnClient(a model.Agent, remote bool) bool {
	if a.Host == "" {
		return !remote
	}
	return a.Host == Host()
}

// Where describes the machine a runs on for messages.
func Where(a model.Agent) string {
	if a.Host == "" {
		return "the server's machine"
	}
	return "host '" + a.Host + "'"
}
