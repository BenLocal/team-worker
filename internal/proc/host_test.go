package proc

import (
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestHostChecks(t *testing.T) {
	here := model.Agent{Host: Host()}
	unset := model.Agent{}
	elsewhere := model.Agent{Host: "other-machine.example"}
	cases := []struct {
		name               string
		a                  model.Agent
		server, local, rem bool
	}{
		{"this machine", here, true, true, true},
		{"no host (recorded beside the server)", unset, true, true, false},
		{"another machine", elsewhere, false, false, false},
	}
	for _, c := range cases {
		if got := OnServer(c.a); got != c.server {
			t.Errorf("%s: OnServer = %v", c.name, got)
		}
		if got := OnClient(c.a, false); got != c.local {
			t.Errorf("%s: OnClient(local) = %v", c.name, got)
		}
		if got := OnClient(c.a, true); got != c.rem {
			t.Errorf("%s: OnClient(remote) = %v", c.name, got)
		}
	}
	if Where(unset) != "the server's machine" || Where(elsewhere) != "host 'other-machine.example'" {
		t.Errorf("Where = %q, %q", Where(unset), Where(elsewhere))
	}
}
