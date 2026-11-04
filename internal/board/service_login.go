package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/config"
)

// openbaoAddress is the OpenBao server the image runs; tests point it elsewhere.
var openbaoAddress = "http://127.0.0.1:8200"

// openbaoUI is where the image's Nginx serves the OpenBao UI.
const openbaoUI = "/ui/"

// magpieAuth answers Nginx's auth_request for /magpie/ when
// TEAM_WORKER_MAGPIE_SESSION_LOGIN=1: reaching it means the board session is
// valid, and the Magpie key it returns in a header becomes Magpie's sign-in
// cookie, so a signed-in viewer never types it.
func magpieAuth(w http.ResponseWriter, r *http.Request) {
	if !config.MagpieSessionLogin() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(config.MagpieWebKeyPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reply(w, nil, fmt.Errorf("unable to read Magpie Web Key"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if key := strings.TrimSpace(string(data)); key != "" {
		w.Header().Set("X-Magpie-Key", key)
	}
	w.WriteHeader(http.StatusNoContent)
}

// openbaoLogin signs a board viewer in to the OpenBao UI when
// TEAM_WORKER_OPENBAO_SESSION_LOGIN=1. It creates a child of the saved root
// token that expires with the board session, wraps it for a minute and hands
// the UI the wrapping token, which it unwraps once. With manual setup there
// is no saved root token, so the viewer signs in there.
func openbaoLogin(w http.ResponseWriter, r *http.Request) {
	if !config.OpenBaoSessionLogin() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	data, err := os.ReadFile(config.OpenBaoInitPath())
	if errors.Is(err, os.ErrNotExist) {
		http.Redirect(w, r, openbaoUI, http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "Unable to read the OpenBao root token", http.StatusInternalServerError)
		return
	}
	var saved struct {
		RootToken string `json:"root_token"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.RootToken == "" {
		http.Error(w, "Unable to decode the OpenBao root token", http.StatusInternalServerError)
		return
	}
	wrapped, err := wrappedChildToken(r, saved.RootToken)
	if err != nil {
		http.Error(w, "Unable to sign in to OpenBao: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, openbaoUI+"vault/auth?with=token&wrapped_token="+url.QueryEscape(wrapped), http.StatusSeeOther)
}

func wrappedChildToken(r *http.Request, root string) (string, error) {
	body, err := json.Marshal(map[string]any{"ttl": "12h", "display_name": "board"})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, openbaoAddress+"/v1/auth/token/create", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", root)
	req.Header.Set("X-Vault-Wrap-TTL", "60s")
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenBao is not reachable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// The body is not echoed: it can repeat request values.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenBao answered %d", resp.StatusCode)
	}
	var out struct {
		WrapInfo struct {
			Token string `json:"token"`
		} `json:"wrap_info"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.WrapInfo.Token == "" {
		return "", fmt.Errorf("OpenBao returned no wrapped token")
	}
	return out.WrapInfo.Token, nil
}
