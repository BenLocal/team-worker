package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// errYAML hides parse errors, which quote their input and may contain keys.
var errYAML = errors.New("invalid YAML")

// loadYAML parses a YAML file into maps, lists and scalars.
// An empty file yields nil.
func loadYAML(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, errYAML
	}
	return v, nil
}

// asMap returns v as a string-keyed map.
func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// keysWithin reports whether every key of m is in allowed.
func keysWithin(m map[string]any, allowed ...string) bool {
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// keysExactly reports whether m has exactly the given keys.
func keysExactly(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	return keysWithin(m, keys...)
}

// stringList converts v to a list of non-blank strings.
func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// stringMap converts v to a map of non-blank keys to string values.
func stringMap(v any) (map[string]string, bool) {
	m, ok := asMap(v)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(m))
	for k, item := range m {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(k) == "" {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func configHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".config")
}

// writeAtomic writes data to a temporary sibling with mode and renames it
// over path, creating parent directories with 0700.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSONFile decodes path into a map; a missing file yields an empty map.
func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s is not a valid JSON object", path)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// downloadEnv returns the process environment with MIRROR_HTTP_PROXY, when
// set, as the HTTP(S) proxy; it is only for install commands' children.
func downloadEnv() []string {
	mirror := os.Getenv("MIRROR_HTTP_PROXY")
	if mirror == "" {
		return os.Environ()
	}
	override := map[string]bool{"HTTP_PROXY": true, "HTTPS_PROXY": true, "http_proxy": true, "https_proxy": true}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !override[name] {
			env = append(env, kv)
		}
	}
	for name := range override {
		env = append(env, name+"="+mirror)
	}
	return env
}

// noProxyClient talks to loopback services, bypassing deployment proxies.
func noProxyClient(timeout time.Duration, jar http.CookieJar) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Jar:       jar,
		Transport: &http.Transport{Proxy: nil},
	}
}

const magpieWebBase = "http://127.0.0.1:3430"

// magpieWebKey reads the Magpie Web sign-in key from the environment or the
// file the entrypoint keeps in the home volume.
func magpieWebKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("MAGPIE_WEB_KEY")); k != "" {
		return k, nil
	}
	data, err := os.ReadFile(filepath.Join(homeDir(), ".config/magpie/web.key"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Library is a signed-in session with Magpie's Web Library API.
type Library struct {
	base   string
	client *http.Client
}

// newLibrary signs in with key; the session cookie stays in the client's jar.
func newLibrary(base, key string) (*Library, error) {
	jar, _ := cookiejar.New(nil)
	l := &Library{base: base, client: noProxyClient(180*time.Second, jar)}
	if _, err := l.do("/?k="+url.QueryEscape(key), nil, true); err != nil {
		return nil, err
	}
	return l, nil
}

// Request GETs route, or POSTs data as JSON when data is non-nil, and
// decodes the JSON response.
func (l *Library) Request(route string, data any) (any, error) {
	return l.do(route, data, false)
}

func (l *Library) do(route string, data any, login bool) (any, error) {
	method := http.MethodGet
	var body io.Reader
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		method, body = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, l.base+route, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", l.base)
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, errors.New("cannot reach Magpie Library or it returned invalid JSON")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Responses may contain private repository addresses or credentials.
		return nil, fmt.Errorf("Magpie Library request failed with HTTP %d", resp.StatusCode)
	}
	if login {
		return nil, nil
	}
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, errors.New("cannot reach Magpie Library or it returned invalid JSON")
	}
	return v, nil
}

// View requests route and requires a JSON object in return.
func (l *Library) View(route string, data any) (map[string]any, error) {
	v, err := l.Request(route, data)
	if err != nil {
		return nil, err
	}
	m, ok := asMap(v)
	if !ok {
		return nil, errors.New("Magpie Library returned an unexpected response")
	}
	return m, nil
}

// Change runs a Library operation under /api/library/ and fails unless every
// part of it completed.
func (l *Library) Change(route string, data any) (map[string]any, error) {
	view, err := l.View("/api/library/"+route, data)
	if err != nil {
		return nil, err
	}
	result, ok := asMap(view["result"])
	if !ok {
		return nil, errors.New("Magpie Library returned no operation result")
	}
	if truthy(view["problems"]) || truthy(result["skipped"]) || truthy(result["unupdated"]) || truthy(result["gone"]) {
		return nil, errors.New("Magpie Library did not finish every install, update or sync; check the connection and local edits")
	}
	return result, nil
}

// truthy follows Python truthiness for decoded JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// agentIDs collects field from every object in list, e.g. agent ids.
func agentIDs(list any, field string) map[string]bool {
	out := map[string]bool{}
	items, _ := list.([]any)
	for _, item := range items {
		if m, ok := asMap(item); ok {
			if s, ok := m[field].(string); ok {
				out[s] = true
			}
		}
	}
	return out
}
