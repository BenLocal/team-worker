package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func shell(script string) []any { return []any{"sh", "-c", script} }

func TestMCPInstallsOnceRechecksAndReinstallsChangedCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	marker, counter := filepath.Join(root, "installed"), filepath.Join(root, "count")
	install := map[string]any{
		"command": shell("touch " + marker + "; printf x >> " + counter),
		"check":   shell("test -e " + marker),
	}
	server := map[string]any{"name": "fixture", "install": install}
	for range 2 {
		if err := installMCPServer(server, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(counter); string(got) != "x" {
		t.Fatalf("installed %q times, want once", got)
	}
	install["command"] = shell("touch " + marker + "; printf x >> " + counter + "; echo new version")
	if err := installMCPServer(server, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(counter); string(got) != "xx" {
		t.Fatalf("changed command ran %q times, want a second install", got)
	}
}

func TestMCPFailedInstallIsNotCached(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	server := map[string]any{"name": "fails", "install": map[string]any{
		"command": shell("exit 2"), "check": shell("exit 1"),
	}}
	err := installMCPServer(server, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "install failed") {
		t.Fatalf("err = %v, want an install failure", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*/*/*.json"))
	if len(matches) != 0 {
		t.Errorf("failed install recorded: %v", matches)
	}
}

func TestMCPAllServersValidatedBeforeNetwork(t *testing.T) {
	t.Setenv("MAGPIE_WEB_KEY", "test")
	good := `{"name": "good", "transport": "stdio", "command": "python3", "agents": ["claude"]}`
	for _, invalid := range []string{
		`{}`,
		`{"name": "good2", "transport": "stdio", "command": "python3", "agents": ["claude"], "args": "wrong"}`,
		`{"name": "good2", "transport": "stdio", "command": "python3", "agents": ["claude"], "env": {"KEY": false}}`,
		`{"name": "good2", "transport": "stdio", "command": "python3", "agents": ["claude"], "url": "http://example.com"}`,
		`{"name": "bad", "transport": "http", "url": "file:///tmp/mcp", "agents": ["claude"]}`,
	} {
		lib := newFakeLibrary(t)
		path := writeFile(t, t.TempDir(), "mcp.yaml", `{"servers": [`+good+`, `+invalid+`]}`)
		if err := configureMCP(lib.server.URL, path, io.Discard); err == nil {
			t.Errorf("%s: accepted", invalid)
		}
		if n := lib.requestCount(); n != 0 {
			t.Errorf("%s: %d requests before validation finished", invalid, n)
		}
	}
}

func TestMCPUpsertsNamedServerWithoutRemovingUnlistedOnes(t *testing.T) {
	server := `{"name": "configured", "transport": "stdio", "command": "python3", "agents": ["claude"]}`
	lib := newFakeLibrary(t)
	lib.respond("GET /api/library", `{"agents": [{"id": "claude"}], "servers": [{"name": "configured"}, {"name": "manual"}]}`)
	lib.respond("POST /api/library/servers/save", `{"result": {}}`)
	t.Setenv("MAGPIE_WEB_KEY", "test")
	path := writeFile(t, t.TempDir(), "mcp.yaml", `{"servers": [`+server+`]}`)
	if err := configureMCP(lib.server.URL, path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(lib.posts) != 1 || lib.posts[0] != "/api/library/servers/save" {
		t.Fatalf("posts = %v, want one servers/save", lib.posts)
	}
	var want any
	json.Unmarshal([]byte(`{"old": "configured", "server": `+server+`}`), &want)
	if got := lib.bodies["/api/library/servers/save"]; !reflect.DeepEqual(got, want) {
		t.Errorf("saved %v, want %v", got, want)
	}
}

func TestMCPEmptyConfigDoesNotContactMagpie(t *testing.T) {
	lib := newFakeLibrary(t)
	path := writeFile(t, t.TempDir(), "mcp.yaml", "servers: []\n")
	if err := configureMCP(lib.server.URL, path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := lib.requestCount(); n != 0 {
		t.Errorf("%d requests for an empty config", n)
	}
}
