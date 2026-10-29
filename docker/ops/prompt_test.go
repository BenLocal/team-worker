package main

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"
)

func TestPromptEmptyConfigPreservesExistingProfileWithoutNetwork(t *testing.T) {
	lib := newFakeLibrary(t)
	path := writeFile(t, t.TempDir(), "prompt.yaml", "instructions: {}\n")
	if err := configurePrompt(lib.server.URL, path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := lib.requestCount(); n != 0 {
		t.Errorf("%d requests for an empty config", n)
	}
}

func TestPromptInvalidFieldsRejectedBeforeNetwork(t *testing.T) {
	t.Setenv("MAGPIE_WEB_KEY", "test")
	for _, value := range []string{
		`null`, `[]`, `{"typo": "x"}`, `{"shared": true}`, `{"agents": "claude"}`, `{"extra": {"claude": false}}`,
	} {
		lib := newFakeLibrary(t)
		path := writeFile(t, t.TempDir(), "prompt.yaml", `{"instructions": `+value+`}`)
		if err := configurePrompt(lib.server.URL, path, io.Discard); err == nil {
			t.Errorf("%s: accepted", value)
		}
		if n := lib.requestCount(); n != 0 {
			t.Errorf("%s: %d requests before validation finished", value, n)
		}
	}
}

func TestPromptPartialConfigAndExplicitEmptyValuesPassThrough(t *testing.T) {
	t.Setenv("MAGPIE_WEB_KEY", "test")
	for _, value := range []string{`{"shared": ""}`, `{"agents": []}`, `{"extra": {"claude": ""}}`} {
		lib := newFakeLibrary(t)
		lib.respond("GET /api/library", `{"instructions": {"agents": [{"agent": "claude"}]}}`)
		lib.respond("POST /api/library/instructions/save", `{"result": {"problems": []}}`)
		path := writeFile(t, t.TempDir(), "prompt.yaml", `{"instructions": `+value+`}`)
		if err := configurePrompt(lib.server.URL, path, io.Discard); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		var want any
		json.Unmarshal([]byte(value), &want)
		if got := lib.bodies["/api/library/instructions/save"]; !reflect.DeepEqual(got, want) {
			t.Errorf("saved %v, want %v", got, want)
		}
	}
}

func TestPromptUnknownAgentIsNotSaved(t *testing.T) {
	lib := newFakeLibrary(t)
	lib.respond("GET /api/library", `{"instructions": {"agents": [{"agent": "claude"}]}}`)
	t.Setenv("MAGPIE_WEB_KEY", "test")
	path := writeFile(t, t.TempDir(), "prompt.yaml", "instructions: {extra: {cluade: hello}}\n")
	if err := configurePrompt(lib.server.URL, path, io.Discard); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if len(lib.posts) != 0 {
		t.Errorf("posts = %v, want none", lib.posts)
	}
}
