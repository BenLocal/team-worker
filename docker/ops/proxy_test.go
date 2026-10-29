package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readDockerConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func defaultProxies(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	return config["proxies"].(map[string]any)["default"].(map[string]any)
}

func TestDockerProxyNormalizationLeavesApplicationEnvironment(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"DOCKER_CONFIG":     dir,
		"MIRROR_HTTP_PROXY": "http://mirror:3128",
		"HTTP_PROXY":        "http://application:8080",
		"NO_PROXY":          "registry.internal",
	}
	original := map[string]string{}
	for k, v := range env {
		original[k] = v
	}
	if err := configureDockerProxy(env); err != nil {
		t.Fatal(err)
	}
	if len(env) != len(original) || env["HTTP_PROXY"] != original["HTTP_PROXY"] {
		t.Fatalf("environment changed: %v", env)
	}
	proxies := defaultProxies(t, readDockerConfig(t, dir))
	if proxies["httpProxy"] != "http://mirror:3128" {
		t.Fatalf("httpProxy = %v", proxies["httpProxy"])
	}
	noProxy := proxies["noProxy"].(string)
	if !strings.Contains(noProxy, "registry.internal") || !strings.Contains(noProxy, "127.0.0.1") {
		t.Fatalf("noProxy = %q", noProxy)
	}
}

func TestDownloadEnvUsesMirrorOnlyForChildren(t *testing.T) {
	t.Setenv("MIRROR_HTTP_PROXY", "http://mirror:3128")
	t.Setenv("HTTPS_PROXY", "http://application:8080")
	child := downloadEnv()
	if !slices.Contains(child, "HTTPS_PROXY=http://mirror:3128") || slices.Contains(child, "HTTPS_PROXY=http://application:8080") {
		t.Fatalf("child environment = %v", child)
	}
	if os.Getenv("HTTPS_PROXY") != "http://application:8080" {
		t.Fatal("process environment changed")
	}
}

func TestDockerProxyPreservesRegistryAuthAndOtherDaemons(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	initial := `{"auths":{"registry.example":{"auth":"existing-login"}},"proxies":{"tcp://other:2376":{"noProxy":"other"}}}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	err := configureDockerProxy(map[string]string{
		"DOCKER_CONFIG": dir,
		"HTTP_PROXY":    "http://proxy:3128",
		"HTTPS_PROXY":   "http://proxy:3128",
		"NO_PROXY":      "localhost,127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	config := readDockerConfig(t, dir)
	auth := config["auths"].(map[string]any)["registry.example"].(map[string]any)["auth"]
	if auth != "existing-login" {
		t.Fatalf("auths changed: %v", config["auths"])
	}
	other := config["proxies"].(map[string]any)["tcp://other:2376"].(map[string]any)
	if len(other) != 1 || other["noProxy"] != "other" {
		t.Fatalf("other daemon changed: %v", other)
	}
	if got := defaultProxies(t, config)["httpProxy"]; got != "http://proxy:3128" {
		t.Fatalf("httpProxy = %v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestClearingRuntimeProxyRemovesPreviousValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"proxies":{"default":{"httpProxy":"http://old:3128"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureDockerProxy(map[string]string{"DOCKER_CONFIG": dir}); err != nil {
		t.Fatal(err)
	}
	proxies := defaultProxies(t, readDockerConfig(t, dir))
	if len(proxies) != 1 || proxies["noProxy"] != "localhost,127.0.0.1,::1" {
		t.Fatalf("default proxies = %v", proxies)
	}
}

func TestInvalidDockerConfigIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureDockerProxy(map[string]string{"DOCKER_CONFIG": dir}); err == nil {
		t.Fatal("expected an error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "invalid json" {
		t.Fatalf("config overwritten: %q", data)
	}
}
