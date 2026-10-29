package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/benlocal/team-worker/internal/config"
)

type serviceKey struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
}

// serviceKeys is registered inside the authenticated board and read on demand.
func serviceKeys(w http.ResponseWriter, r *http.Request) {
	keys := []serviceKey{{Name: "Magpie", Label: "Web Key"}, {Name: "OpenBao", Label: "Login token"}}
	data, err := os.ReadFile(config.MagpieWebKeyPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reply(w, nil, fmt.Errorf("unable to read Magpie Web Key"))
		return
	}
	keys[0].Value = strings.TrimSpace(string(data))
	data, err = os.ReadFile(config.OpenBaoInitPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reply(w, nil, fmt.Errorf("unable to read OpenBao login token"))
		return
	}
	if err == nil {
		var saved struct {
			RootToken string `json:"root_token"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			reply(w, nil, fmt.Errorf("unable to decode OpenBao login token"))
			return
		}
		keys[1].Value = saved.RootToken
	}
	reply(w, keys, nil)
}
