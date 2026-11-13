package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func testIdentity() model.MemoryIdentity { return model.MemoryIdentity{Team: "demo", Name: "worker"} }
func mustRead(t *testing.T, m *Manager, id model.MemoryIdentity, p string) model.MemoryDocument {
	t.Helper()
	d, err := m.Read(id, p)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFilesSurviveReopenAndKeepIdentitiesSeparate(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	id := testIdentity()
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	d := mustRead(t, m, id, "MEMORY.md")
	if _, err := m.Write(id, d.Path, "# Memory\nA confirmed fact.\n", d.Hash, "test"); err != nil {
		t.Fatal(err)
	}
	m = New(dir)
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, m, id, d.Path); !strings.Contains(got.Content, "confirmed fact") {
		t.Fatal(got)
	}
	for _, other := range []model.MemoryIdentity{{Team: "demo", Name: "worker", User: "other"}, {Team: "other", Name: "worker"}, {Team: "demo", Name: "worker/../peer"}} {
		if err := m.Ensure(other); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(mustRead(t, m, other, d.Path).Content, "confirmed fact") {
			t.Fatal("identity leak")
		}
	}
	ids, err := m.Identities()
	if err != nil || len(ids) != 4 {
		t.Fatalf("identities=%v err=%v", ids, err)
	}
}

func TestWritesConflictAndRollbackPreservesHistory(t *testing.T) {
	m := New(t.TempDir())
	id := testIdentity()
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	original := mustRead(t, m, id, "MEMORY.md")
	changed, err := m.Write(id, original.Path, "new", original.Hash, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Write(id, original.Path, "stale", original.Hash, "test"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	history, err := m.History(id, original.Path)
	if err != nil {
		t.Fatal(err)
	}
	var rev string
	for _, v := range history {
		if v.Content == original.Content {
			rev = v.ID
			break
		}
	}
	if rev == "" {
		t.Fatal("missing original revision")
	}
	restored, err := m.Rollback(id, original.Path, rev, changed.Hash)
	if err != nil || restored.Content != original.Content {
		t.Fatalf("%+v %v", restored, err)
	}
	history, err = m.History(id, original.Path)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].Source != "rollback "+rev {
		t.Fatal(history)
	}
}

func TestRejectsTraversalAndSymlinks(t *testing.T) {
	m := New(t.TempDir())
	id := testIdentity()
	if err := m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../MEMORY.md", "/tmp/MEMORY.md", "notes/../../MEMORY.md", "notes\\bad.md", "history/hidden.md", "notes/file.txt"} {
		if _, err := m.Write(id, p, "bad", "", "test"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	dir, err := m.Directory(id)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err = os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "notes", "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Read(id, "notes/link.md"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = m.Write(id, "notes/link.md", "bad", "", "test"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("outside changed: %s %v", data, err)
	}
}

func TestContextBudgetAndAcknowledgment(t *testing.T) {
	m := New(t.TempDir())
	id := testIdentity()
	text, err := m.Context(id, "run-a", true)
	if err != nil || !strings.Contains(text, "will be initialized") {
		t.Fatalf("%s %v", text, err)
	}
	dir, _ := m.Directory(id)
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dry run created files")
	}
	if err = m.Ensure(id); err != nil {
		t.Fatal(err)
	}
	d := mustRead(t, m, id, "AGENTS.md")
	if _, err = m.Write(id, d.Path, strings.Repeat("x", ContextBytes+1), d.Hash, "test"); err != nil {
		t.Fatal(err)
	}
	text, err = m.Context(id, "run-a", false)
	if err != nil || !strings.Contains(text, "Content omitted") {
		t.Fatalf("budget: %v", err)
	}
	st, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Pending || st.RunID != "run-a" {
		t.Fatal(st)
	}
	if err = m.Acknowledge(id, "old-run", st.Current); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = m.Acknowledge(id, "run-a", st.Current); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(id)
	if err != nil || st.Pending {
		t.Fatalf("ack state: %+v %v", st, err)
	}
	// A direct editor's changes must be archived and invalidate the acknowledgment.
	if err = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("direct edit"), 0600); err != nil {
		t.Fatal(err)
	}
	provided := st.Provided["MEMORY.md"]
	if _, err = m.Context(id, "", false); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(id)
	if err != nil || !st.Pending {
		t.Fatalf("direct edit: %+v %v", st, err)
	}
	if st.RunID != "run-a" || st.Provided["MEMORY.md"] != provided {
		t.Fatal("operator context consumed the member's pending reload")
	}
	history, err := m.History(id, "MEMORY.md")
	if err != nil || history[0].Content != "direct edit" {
		t.Fatalf("%+v %v", history, err)
	}
}
