package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// openbaoCmd imports openbao.yaml into OpenBao, then runs OpenBao Agent to
// render secret files.
//
// By default initialization and unsealing stay manual: it waits until
// OpenBao is unsealed and the import token file exists. With
// TEAM_WORKER_OPENBAO_AUTO_UNSEAL=1 it initializes once, keeps the unseal key
// and root token in ~/.openbao/init.json and unseals on every start. It then
// writes the declared KV secrets, grants a read-only AppRole to the agent,
// and execs `bao agent` to render the files.
func openbaoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "openbao FILE",
		Short: "Import openbao.yaml into OpenBao and run OpenBao Agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("TEAM_WORKER_OPENBAO") != "1" {
				return nil
			}
			tokenFile := os.Getenv("TEAM_WORKER_OPENBAO_TOKEN_FILE")
			if tokenFile == "" {
				tokenFile = "/etc/team-worker/openbao.token"
			}
			path, err := configureOpenBao(args[0], tokenFile, homeDir(),
				os.Getenv("TEAM_WORKER_OPENBAO_AUTO_UNSEAL") == "1")
			if err != nil {
				return failWith("OpenBao import failed: ", err)
			}
			if path == "" {
				return nil
			}
			fmt.Println("openbao: starting OpenBao Agent to render files.")
			bao, err := exec.LookPath("bao")
			if err != nil {
				return failWith("OpenBao import failed: ", err)
			}
			return syscall.Exec(bao, []string{"bao", "agent", "-config=" + path}, os.Environ())
		},
	}
}

const (
	openbaoMount = "secret"
	openbaoRole  = "team-worker-agent"
)

var (
	// openbaoAddress and openbaoPoll are variables so tests can use a fake server.
	openbaoAddress = "http://127.0.0.1:8200"
	openbaoPoll    = 10 * time.Second
	secretPath     = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
)

type openbaoFile struct {
	Path     string
	Template string
}

// readOpenBaoConfig validates the whole file before touching OpenBao.
func readOpenBaoConfig(path string) (map[string]map[string]string, []openbaoFile, error) {
	raw, err := loadYAML(path)
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	config, ok := asMap(raw)
	if !ok || !keysWithin(config, "secrets", "files") {
		return nil, nil, errors.New("openbao.yaml only supports secrets and files")
	}
	secrets := map[string]map[string]string{}
	if config["secrets"] != nil {
		m, ok := asMap(config["secrets"])
		if !ok {
			return nil, nil, errors.New("secrets must map paths to key-value pairs")
		}
		for name, data := range m {
			// "." and ".." segments would resolve to other API endpoints in the URL.
			segments := strings.Split(name, "/")
			if !secretPath.MatchString(name) || containsString(segments, ".") || containsString(segments, "..") {
				return nil, nil, errors.New("secret paths may only contain letters, digits, . _ - and /, without . or .. segments")
			}
			values, ok := asMap(data)
			if !ok || len(values) == 0 {
				return nil, nil, fmt.Errorf("secrets.%s must be a non-empty map of string keys to string values", name)
			}
			secret := map[string]string{}
			for k, v := range values {
				s, ok := v.(string)
				if k == "" || !ok {
					return nil, nil, fmt.Errorf("secrets.%s must be a non-empty map of string keys to string values", name)
				}
				secret[k] = s
			}
			secrets[name] = secret
		}
	}
	var files []openbaoFile
	if config["files"] != nil {
		items, ok := config["files"].([]any)
		if !ok {
			return nil, nil, errors.New("files must be a list")
		}
		seen := map[string]bool{}
		for _, item := range items {
			m, ok := asMap(item)
			var p, tmpl string
			if ok && keysExactly(m, "path", "template") {
				p, _ = m["path"].(string)
				tmpl, _ = m["template"].(string)
			}
			if !strings.HasPrefix(p, "/") || strings.TrimSpace(tmpl) == "" {
				return nil, nil, errors.New("each files entry must contain only an absolute path and a non-empty template")
			}
			if seen[p] {
				return nil, nil, errors.New("files paths must be unique")
			}
			seen[p] = true
			files = append(files, openbaoFile{Path: p, Template: tmpl})
		}
	}
	return secrets, files, nil
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// baoClient calls the OpenBao HTTP API, bypassing deployment proxies.
type baoClient struct {
	address string
	token   string
	http    *http.Client
}

func newBaoClient(address string) *baoClient {
	return &baoClient{address: address, http: noProxyClient(15*time.Second, nil)}
}

// call sends payload as JSON and decodes the response. A GET answered with
// 404 returns nil, nil. Errors report the status only, since bodies can echo
// request values.
func (c *baoClient) call(method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.address+"/v1/"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusNotFound && method == http.MethodGet {
			return nil, nil
		}
		return nil, &baoStatusError{method: method, path: path, status: resp.StatusCode}
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("OpenBao %s %s returned invalid JSON", method, path)
		}
	}
	return out, nil
}

type baoStatusError struct {
	method, path string
	status       int
}

func (e *baoStatusError) Error() string {
	return fmt.Sprintf("OpenBao %s %s returned %d", e.method, e.path, e.status)
}

// health returns "ready", or the reason OpenBao cannot be used yet.
func (c *baoClient) health() string {
	resp, err := c.http.Get(c.address + "/v1/sys/health")
	if err != nil {
		return "not started"
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		return "ready"
	}
	switch resp.StatusCode {
	case 501:
		return "not initialized"
	case 503:
		return "sealed"
	case 429:
		return "standby node"
	}
	return fmt.Sprintf("status %d", resp.StatusCode)
}

// waitUntil polls check until it returns "ready", logging each new state once.
func waitUntil(check func() string, describe string) {
	last := ""
	for {
		state := check()
		if state == "ready" {
			return
		}
		if state != last {
			fmt.Printf("openbao: waiting for %s (%s).\n", describe, state)
			last = state
		}
		time.Sleep(openbaoPoll)
	}
}

func tokenState(tokenFile string) string {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "missing"
	}
	if strings.TrimSpace(string(data)) == "" {
		return "empty"
	}
	return "ready"
}

// dataOrSelf returns m["data"] when it is an object, else m; list endpoints
// answer either way depending on the OpenBao version.
func dataOrSelf(m map[string]any) map[string]any {
	if d, ok := asMap(m["data"]); ok {
		return d
	}
	return m
}

func importSecrets(c *baoClient, secrets map[string]map[string]string) error {
	mounts, err := c.call("GET", "sys/mounts", nil)
	if err != nil {
		return err
	}
	if _, ok := dataOrSelf(mounts)[openbaoMount+"/"]; !ok {
		if _, err := c.call("POST", "sys/mounts/"+openbaoMount,
			map[string]any{"type": "kv", "options": map[string]any{"version": "2"}}); err != nil {
			return err
		}
		// A new KV v2 mount upgrades in the background and answers 400 until ready.
		for attempt := 0; ; attempt++ {
			_, err := c.call("GET", openbaoMount+"/config", nil)
			if err == nil {
				break
			}
			var status *baoStatusError
			if !errors.As(err, &status) || attempt == 29 {
				return err
			}
			time.Sleep(time.Second)
		}
	}
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	var rules []string
	for _, name := range names {
		current, err := c.call("GET", openbaoMount+"/data/"+name, nil)
		if err != nil {
			return err
		}
		if !sameSecret(current, secrets[name]) {
			if _, err := c.call("POST", openbaoMount+"/data/"+name,
				map[string]any{"data": secrets[name]}); err != nil {
				return err
			}
		}
		rules = append(rules, fmt.Sprintf(`path "%s/data/%s" { capabilities = ["read"] }`, openbaoMount, name))
	}
	policy := strings.Join(rules, "\n")
	if policy == "" {
		policy = "# none"
	}
	if _, err := c.call("PUT", "sys/policies/acl/"+openbaoRole, map[string]any{"policy": policy}); err != nil {
		return err
	}
	auth, err := c.call("GET", "sys/auth", nil)
	if err != nil {
		return err
	}
	if _, ok := dataOrSelf(auth)["approle/"]; !ok {
		if _, err := c.call("POST", "sys/auth/approle", map[string]any{"type": "approle"}); err != nil {
			return err
		}
	}
	_, err = c.call("POST", "auth/approle/role/"+openbaoRole, map[string]any{
		"token_policies": []string{openbaoRole}, "token_ttl": "1h", "token_max_ttl": "24h",
	})
	return err
}

// sameSecret reports whether a KV v2 read response holds exactly want.
func sameSecret(current map[string]any, want map[string]string) bool {
	outer, _ := asMap(current["data"])
	data, _ := asMap(outer["data"])
	if data == nil {
		return false
	}
	got := map[string]string{}
	for k, v := range data {
		s, ok := v.(string)
		if !ok {
			return false
		}
		got[k] = s
	}
	return reflect.DeepEqual(got, want)
}

func agentConfig(home string, files []openbaoFile) map[string]any {
	state := filepath.Join(home, ".openbao/agent")
	templates := make([]map[string]any, 0, len(files))
	for _, f := range files {
		templates = append(templates, map[string]any{
			"contents":         f.Template,
			"destination":      f.Path,
			"perms":            "0600",
			"create_dest_dirs": true,
		})
	}
	return map[string]any{
		"vault": map[string]any{"address": openbaoAddress},
		"auto_auth": map[string]any{
			"method": []map[string]any{{
				"type": "approle",
				"config": map[string]any{
					"role_id_file_path":                   filepath.Join(state, "role_id"),
					"secret_id_file_path":                 filepath.Join(state, "secret_id"),
					"remove_secret_id_file_after_reading": false,
				},
			}},
		},
		"template": templates,
	}
}

type initKeys struct {
	KeysBase64 []string `json:"keys_base64"`
	RootToken  string   `json:"root_token"`
}

// autoUnsealState initializes once and unseals with the stored keys; it
// returns "ready" once OpenBao is unsealed.
func autoUnsealState(c *baoClient, initFile string) string {
	status, err := c.call("GET", "sys/seal-status", nil)
	if err != nil || status == nil {
		return "not started"
	}
	if initialized, _ := status["initialized"].(bool); !initialized {
		if _, err := os.Stat(initFile); err == nil {
			// The data was reset; keep the old keys instead of overwriting them.
			backup := fmt.Sprintf("%s.bak-%d", initFile, time.Now().Unix())
			if err := os.Rename(initFile, backup); err != nil {
				return "cannot back up " + initFile
			}
		}
		result, err := c.call("PUT", "sys/init", map[string]any{"secret_shares": 1, "secret_threshold": 1})
		if err != nil {
			return "initialization failed"
		}
		var keys initKeys
		raw, _ := json.Marshal(result)
		if err := json.Unmarshal(raw, &keys); err != nil {
			return "initialization returned no keys"
		}
		data, _ := json.Marshal(keys)
		if err := writeAtomic(initFile, data, 0o600); err != nil {
			return "cannot save " + initFile
		}
		fmt.Printf("openbao: initialized automatically; the unseal key and root token are saved in %s.\n", initFile)
		return "just initialized"
	}
	if sealed, _ := status["sealed"].(bool); sealed {
		data, err := os.ReadFile(initFile)
		if err != nil {
			return fmt.Sprintf("initialized but %s is missing; unseal it manually", initFile)
		}
		var keys initKeys
		if err := json.Unmarshal(data, &keys); err != nil {
			return "cannot read " + initFile
		}
		threshold := len(keys.KeysBase64)
		if t, ok := status["t"].(float64); ok && t > 0 && int(t) < threshold {
			threshold = int(t)
		}
		for _, key := range keys.KeysBase64[:threshold] {
			if _, err := c.call("PUT", "sys/unseal", map[string]any{"key": key}); err != nil {
				return "unseal failed"
			}
		}
		return "unsealing"
	}
	return "ready"
}

// configureOpenBao imports the secrets and prepares the agent; it returns the
// agent config path, or "" when there are no files to render.
func configureOpenBao(configPath, tokenFile, home string, autoUnseal bool) (string, error) {
	secrets, files, err := readOpenBaoConfig(configPath)
	if err != nil {
		return "", err
	}
	if !autoUnseal && len(secrets) == 0 && len(files) == 0 {
		fmt.Println("openbao: openbao.yaml declares no secrets or files.")
		return "", nil
	}
	c := newBaoClient(openbaoAddress)
	initFile := filepath.Join(home, ".openbao/init.json")
	if autoUnseal {
		// Initialize even without secrets: an uninitialized server can be claimed by anyone.
		waitUntil(func() string { return autoUnsealState(c, initFile) }, "automatic initialization and unsealing")
	}
	if len(secrets) == 0 && len(files) == 0 {
		fmt.Println("openbao: openbao.yaml declares no secrets or files.")
		return "", nil
	}
	waitUntil(c.health, "OpenBao to be initialized and unsealed")
	if autoUnseal && tokenState(tokenFile) != "ready" {
		data, err := os.ReadFile(initFile)
		if err != nil {
			return "", err
		}
		var keys initKeys
		if err := json.Unmarshal(data, &keys); err != nil {
			return "", fmt.Errorf("cannot read %s", initFile)
		}
		c.token = keys.RootToken
	} else {
		waitUntil(func() string { return tokenState(tokenFile) }, "the import token file "+tokenFile)
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", err
		}
		c.token = strings.TrimSpace(string(data))
	}
	if err := importSecrets(c, secrets); err != nil {
		return "", err
	}
	fmt.Printf("openbao: imported %d secret paths.\n", len(secrets))
	if len(files) == 0 {
		return "", nil
	}
	state := filepath.Join(home, ".openbao/agent")
	roleID, err := c.call("GET", "auth/approle/role/"+openbaoRole+"/role-id", nil)
	if err != nil {
		return "", err
	}
	id, ok := nestedString(roleID, "data", "role_id")
	if !ok {
		return "", errors.New("OpenBao returned no AppRole role_id")
	}
	if err := writeAtomic(filepath.Join(state, "role_id"), []byte(id), 0o600); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(state, "secret_id")); errors.Is(err, os.ErrNotExist) {
		secretID, err := c.call("POST", "auth/approle/role/"+openbaoRole+"/secret-id", nil)
		if err != nil {
			return "", err
		}
		id, ok := nestedString(secretID, "data", "secret_id")
		if !ok {
			return "", errors.New("OpenBao returned no AppRole secret_id")
		}
		if err := writeAtomic(filepath.Join(state, "secret_id"), []byte(id), 0o600); err != nil {
			return "", err
		}
	}
	path := filepath.Join(state, "agent.json")
	data, err := json.MarshalIndent(agentConfig(home, files), "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// nestedString reads m[outer][inner] as a string.
func nestedString(m map[string]any, outer, inner string) (string, bool) {
	o, _ := asMap(m[outer])
	s, ok := o[inner].(string)
	return s, ok
}
