// Package memory manages member-owned Markdown outside disposable workspaces.
package memory

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/benlocal/team-worker/internal/model"
)

const MaxDocumentBytes = 256 << 10
const ContextBytes = 24 << 10

var (
	ErrInvalid  = errors.New("invalid memory request")
	ErrConflict = errors.New("memory version conflict")
	ErrNotFound = errors.New("memory document not found")
)

type Manager struct {
	mu           sync.Mutex
	dataDir      string
	contextBytes int
}

func New(dataDir string) *Manager {
	return NewAtRoot(filepath.Join(dataDir, "agent-data"), ContextBytes)
}

// NewAtRoot uses an explicitly configured personal-data root.
func NewAtRoot(root string, budget int) *Manager {
	if budget < 1024 {
		budget = ContextBytes
	}
	return &Manager{dataDir: root, contextBytes: budget}
}

func identityPath(id model.MemoryIdentity) (string, error) {
	if id.Team == "" || id.Name == "" {
		return "", fmt.Errorf("%w: team and member are required", ErrInvalid)
	}
	parts := []string{}
	for _, value := range []string{id.User, id.Team, id.Name} {
		if len(value) > 100 || !utf8.ValidString(value) {
			return "", fmt.Errorf("%w: identity exceeds 100 bytes or is not UTF-8", ErrInvalid)
		}
		// A prefix distinguishes the empty name and makes every key a local path.
		parts = append(parts, "k"+hex.EncodeToString([]byte(value)))
	}
	return filepath.Join(parts...), nil
}

func (m *Manager) Directory(id model.MemoryIdentity) (string, error) {
	p, err := identityPath(id)
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(m.dataDir, p))
}

// open anchors all file operations to the data directory. Symlinks are
// rejected even inside that root so profiles cannot alias another member.
func (m *Manager) open(id model.MemoryIdentity, create bool) (*os.Root, error) {
	p, err := identityPath(id)
	if err != nil {
		return nil, err
	}
	if create {
		if err = os.MkdirAll(m.dataDir, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(m.dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	prefix := ""
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, part)
		info, e := root.Lstat(prefix)
		if errors.Is(e, fs.ErrNotExist) && create {
			if e = root.Mkdir(prefix, 0700); e != nil && !errors.Is(e, fs.ErrExist) {
				return nil, e
			}
			info, e = root.Lstat(prefix)
		}
		if e != nil {
			return nil, e
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: unsafe profile directory", ErrInvalid)
		}
	}
	profile, err := root.OpenRoot(p)
	if err != nil {
		return nil, err
	}
	if create {
		if err = writeJSON(profile, ".identity.json", id); err != nil {
			profile.Close()
			return nil, err
		}
	}
	return profile, nil
}

func documentPath(p string) error {
	if !fs.ValidPath(p) || strings.Contains(p, `\`) || !strings.HasSuffix(p, ".md") {
		return fmt.Errorf("%w: use a relative Markdown path", ErrInvalid)
	}
	if p == "AGENTS.md" || p == "MEMORY.md" || p == "SCRATCHPAD.md" {
		return nil
	}
	if strings.HasPrefix(p, "notes/") || strings.HasPrefix(p, "daily/") {
		return nil
	}
	return fmt.Errorf("%w: documents must be AGENTS.md, MEMORY.md, SCRATCHPAD.md or under notes/ or daily/", ErrInvalid)
}

func safePath(root *os.Root, p string, createParents bool) error {
	prefix := ""
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: invalid path", ErrInvalid)
		}
		prefix = path.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			if i == len(parts)-1 {
				return nil
			}
			if createParents {
				if err = root.Mkdir(prefix, 0700); err != nil {
					return err
				}
				continue
			}
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not allowed", ErrInvalid)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%w: parent is not a directory", ErrInvalid)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: file is not regular", ErrInvalid)
		}
	}
	return nil
}

func readFile(root *os.Root, p string, limit int64) ([]byte, error) {
	if err := safePath(root, p, false); err != nil {
		return nil, err
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalid, limit)
	}
	return b, nil
}

func newID() string { return rand.Text() }
func Hash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func atomicWrite(root *os.Root, p string, b []byte) error {
	if err := safePath(root, p, true); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(p), ".tmp-"+newID())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = safePath(root, p, false); err != nil {
		return err
	}
	return root.Rename(tmp, p)
}

func writeJSON(root *os.Root, p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(root, p, append(b, '\n'))
}
func readJSON(root *os.Root, p string, v any) error {
	b, err := readFile(root, p, 8<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type fileState struct {
	Observed     map[string]string `json:"observed"`
	Provided     map[string]string `json:"provided"`
	Acknowledged map[string]string `json:"acknowledged"`
	RunID        string            `json:"runId"`
}

func initialize(root *os.Root) error {
	for _, d := range []string{"notes", "daily", "history", ".state"} {
		info, err := root.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			if err = root.Mkdir(d, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: unsafe state directory", ErrInvalid)
		}
	}
	for _, p := range []string{"AGENTS.md", "MEMORY.md", "SCRATCHPAD.md"} {
		f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			if err = safePath(root, p, false); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.WriteString("# " + strings.TrimSuffix(p, ".md") + "\n")
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func documents(root *os.Root, query string) ([]model.MemoryDocument, error) {
	docs := []model.MemoryDocument{}
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && p != "notes" && p != "daily" && !strings.HasPrefix(p, "notes/") && !strings.HasPrefix(p, "daily/") {
				return fs.SkipDir
			}
			return nil
		}
		if documentPath(p) != nil {
			return nil
		}
		b, err := readFile(root, p, MaxDocumentBytes)
		if err != nil {
			return err
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("%w: document is not UTF-8", ErrInvalid)
		}
		if query == "" || strings.Contains(strings.ToLower(p+"\n"+string(b)), strings.ToLower(query)) {
			docs = append(docs, model.MemoryDocument{Path: p, Hash: Hash(string(b)), Content: string(b)})
		}
		return nil
	})
	return docs, err
}

func archive(root *os.Root, doc model.MemoryDocument, source string) error {
	rev := model.MemoryRevision{ID: newID(), Path: doc.Path, At: time.Now().UTC().Format(time.RFC3339Nano), Source: source, Hash: doc.Hash, Content: doc.Content}
	return writeJSON(root, "history/"+rev.ID+".json", rev)
}

func observe(root *os.Root) ([]model.MemoryDocument, fileState, error) {
	var state fileState
	if err := readJSON(root, ".state/files.json", &state); err != nil {
		return nil, state, err
	}
	docs, err := documents(root, "")
	if err != nil {
		return nil, state, err
	}
	hashes := map[string]string{}
	for _, doc := range docs {
		hashes[doc.Path] = doc.Hash
		if state.Observed[doc.Path] != doc.Hash {
			if err = archive(root, doc, "file"); err != nil {
				return nil, state, err
			}
		}
	}
	state.Observed = hashes
	err = writeJSON(root, ".state/files.json", state)
	return docs, state, err
}

func (m *Manager) Ensure(id model.MemoryIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = initialize(root); err != nil {
		return err
	}
	_, _, err = observe(root)
	return err
}

func (m *Manager) Documents(id model.MemoryIdentity, query string) ([]model.MemoryDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return documents(root, query)
}

func (m *Manager) Read(id model.MemoryIdentity, p string) (model.MemoryDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := documentPath(p); err != nil {
		return model.MemoryDocument{}, err
	}
	root, err := m.open(id, false)
	if err != nil {
		return model.MemoryDocument{}, err
	}
	defer root.Close()
	b, err := readFile(root, p, MaxDocumentBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return model.MemoryDocument{}, ErrNotFound
	}
	if err != nil {
		return model.MemoryDocument{}, err
	}
	if !utf8.Valid(b) {
		return model.MemoryDocument{}, fmt.Errorf("%w: document is not UTF-8", ErrInvalid)
	}
	return model.MemoryDocument{Path: p, Content: string(b), Hash: Hash(string(b))}, nil
}

func writeDocument(root *os.Root, p, content, base, source string) (model.MemoryDocument, error) {
	if err := documentPath(p); err != nil {
		return model.MemoryDocument{}, err
	}
	if len(content) > MaxDocumentBytes || !utf8.ValidString(content) {
		return model.MemoryDocument{}, fmt.Errorf("%w: invalid or oversized UTF-8 content", ErrInvalid)
	}
	old, err := readFile(root, p, MaxDocumentBytes)
	current := ""
	if err == nil {
		current = Hash(string(old))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return model.MemoryDocument{}, err
	}
	if current != base {
		return model.MemoryDocument{}, fmt.Errorf("%w: %s", ErrConflict, p)
	}
	doc := model.MemoryDocument{Path: p, Hash: Hash(content), Content: content}
	if current == doc.Hash {
		return doc, nil
	}
	if current != "" {
		if err = archive(root, model.MemoryDocument{Path: p, Hash: current, Content: string(old)}, "before "+source); err != nil {
			return model.MemoryDocument{}, err
		}
	}
	// Recheck after archiving: direct editors do not honor the manager lock.
	again, e := readFile(root, p, MaxDocumentBytes)
	if e != nil && !errors.Is(e, fs.ErrNotExist) {
		return model.MemoryDocument{}, e
	}
	if (e == nil && Hash(string(again)) != current) || (errors.Is(e, fs.ErrNotExist) && current != "") {
		return model.MemoryDocument{}, ErrConflict
	}
	if err = atomicWrite(root, p, []byte(content)); err != nil {
		return model.MemoryDocument{}, err
	}
	if err = archive(root, doc, source); err != nil {
		return model.MemoryDocument{}, err
	}
	return doc, nil
}

func (m *Manager) Write(id model.MemoryIdentity, p, content, base, source string) (model.MemoryDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return model.MemoryDocument{}, err
	}
	defer root.Close()
	return writeDocument(root, p, content, base, source)
}

func revisions(root *os.Root, p string) ([]model.MemoryRevision, error) {
	if err := documentPath(p); err != nil {
		return nil, err
	}
	list, err := fs.ReadDir(root.FS(), "history")
	if err != nil {
		return nil, err
	}
	out := []model.MemoryRevision{}
	for _, entry := range list {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var rev model.MemoryRevision
		if err = readJSON(root, "history/"+entry.Name(), &rev); err != nil {
			return nil, err
		}
		if rev.Path == p {
			out = append(out, rev)
		}
	}
	slices.SortFunc(out, func(a, b model.MemoryRevision) int { return strings.Compare(b.At, a.At) })
	return out, nil
}
func (m *Manager) History(id model.MemoryIdentity, p string) ([]model.MemoryRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return revisions(root, p)
}
func (m *Manager) Rollback(id model.MemoryIdentity, p, revision, base string) (model.MemoryDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return model.MemoryDocument{}, err
	}
	defer root.Close()
	revs, err := revisions(root, p)
	if err != nil {
		return model.MemoryDocument{}, err
	}
	for _, rev := range revs {
		if rev.ID == revision {
			return writeDocument(root, p, rev.Content, base, "rollback "+revision)
		}
	}
	return model.MemoryDocument{}, ErrNotFound
}

func (m *Manager) Status(id model.MemoryIdentity) (model.MemoryStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return model.MemoryStatus{}, err
	}
	defer root.Close()
	_, state, err := observe(root)
	if err != nil {
		return model.MemoryStatus{}, err
	}
	dir, err := m.Directory(id)
	return model.MemoryStatus{Directory: dir, Current: state.Observed, Provided: state.Provided, Acknowledged: state.Acknowledged, RunID: state.RunID, Pending: !maps.Equal(state.Observed, state.Acknowledged)}, err
}

// Context marks content as provided, never as read or obeyed by the model.
func (m *Manager) Context(id model.MemoryIdentity, run string, dry bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, err := m.Directory(id)
	if err != nil {
		return "", err
	}
	root, err := m.open(id, !dry)
	if errors.Is(err, fs.ErrNotExist) && dry {
		return "Personal documents will be initialized at " + dir + ".\n", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	if !dry {
		if err = initialize(root); err != nil {
			return "", err
		}
	}
	docs, err := documents(root, "")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Personal documents\n\nDirectory: %s\nThese documents remain at this absolute path when you change projects. Follow current user instructions and mandatory project rules before personal rules; report conflicts.\nKeep confirmed facts in MEMORY.md, dated logs in daily/, and lessons in notes/. Read older notes on demand. Never store credentials.\nUse team-worker memory read/search with your team, name and --user to retrieve files.\nAfter compaction, run team-worker memory context for your identity again if the executor has no restore hook.\n", dir)

	for _, p := range []string{"AGENTS.md", "MEMORY.md"} {
		for _, doc := range docs {
			if doc.Path != p {
				continue
			}
			fmt.Fprintf(&b, "\n### %s (sha256 %s)\n", p, doc.Hash)
			if b.Len()+len(doc.Content) > m.contextBytes {
				fmt.Fprintf(&b, "Content omitted because of the context budget. Read %s before working; rules were not truncated.\n", filepath.Join(dir, p))
			} else {
				b.WriteString(doc.Content)
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("\nAfter reading the full current files, acknowledge their exact hashes using team-worker memory ack with --run-id and --hashes JSON. Acknowledgment records a read claim, not proof of compliance.\n")
	if !dry {
		_, state, e := observe(root)
		if e != nil {
			return "", e
		}
		// Operator previews must not consume a member's pending reload.
		if run != "" {
			state.Provided = state.Observed
			state.RunID = run
			state.Acknowledged = nil
			if e = writeJSON(root, ".state/files.json", state); e != nil {
				return "", e
			}
		}
	}
	return b.String(), nil
}

func (m *Manager) Acknowledge(id model.MemoryIdentity, run string, hashes map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.open(id, false)
	if err != nil {
		return err
	}
	defer root.Close()
	_, state, err := observe(root)
	if err != nil {
		return err
	}
	if run == "" || state.RunID != run || !maps.Equal(hashes, state.Observed) {
		return fmt.Errorf("%w: acknowledge the current run and all current file hashes", ErrConflict)
	}
	state.Acknowledged = maps.Clone(hashes)
	return writeJSON(root, ".state/files.json", state)
}
