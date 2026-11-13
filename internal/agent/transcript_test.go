package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptEvidenceSelectsExactSessionAndSkipsTools(t *testing.T) {
	for _, client := range []string{"claude", "codex", "pi"} {
		t.Run(client, func(t *testing.T) {
			root := t.TempDir()
			var body string
			switch client {
			case "claude":
				body = `{"sessionId":"example-session","message":{"role":"user","content":"Remember verified correction."}}` + "\n" + `{"sessionId":"example-session","message":{"role":"user","content":[{"type":"tool_result","content":"must not include"}]}}`
			case "pi":
				body = `{"type":"session","id":"example-session"}` + "\n" + `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Remember verified correction."}]}}` + "\n" + `{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"must not include"}]}}`
			case "codex":
				body = `{"type":"session_meta","payload":{"id":"example-session"}}` + "\n" + `{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"Remember verified correction."}]}}` + "\n" + `{"type":"response_item","payload":{"role":"tool","content":[{"type":"output_text","text":"must not include"}]}}`
			}
			if err := os.WriteFile(filepath.Join(root, "example-session.jsonl"), []byte(body+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "other-session.jsonl"), []byte("other member's private text"), 0600); err != nil {
				t.Fatal(err)
			}
			text, err := TranscriptEvidence(root, client, "example-session")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "verified correction") || strings.Contains(text, "must not include") || strings.Contains(text, "private text") {
				t.Fatal(text)
			}
			if _, err = TranscriptEvidence(root, client, "../example-session"); err == nil {
				t.Fatal("accepted traversal")
			}
		})
	}
}
