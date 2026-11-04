package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// CaptureOpencodeSession finds the session an opencode agent started in cwd
// since a time. opencode cannot be given the id of a new session; it keeps
// sessions in its SQLite database (dbPath, opencode.db), one row per session
// with its directory and creation time, and each message part as JSON. Like
// Codex transcripts, a session counts only when one of its parts holds the
// agent's injected identity; subagent sessions are skipped, and several
// matches never fall back to the latest.
func CaptureOpencodeSession(ctx context.Context, dbPath, cwd, agentID string, since time.Time) (string, error) {
	if agentID == "" {
		return "", nil
	}
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	// Read-only: opencode may be writing; the database is never changed here.
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: dbPath}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	needle := "- ID: " + agentID
	rows, err := db.QueryContext(ctx,
		`SELECT s.id, s.directory, p.data FROM session s JOIN part p ON p.session_id = s.id
		 WHERE s.parent_id IS NULL AND s.time_created >= ? AND instr(p.data, ?) > 0`,
		since.UnixMilli(), needle)
	if err != nil {
		return "", fmt.Errorf("read opencode sessions: %w", err)
	}
	defer rows.Close()
	found := ""
	for rows.Next() {
		var id, dir, data string
		if err := rows.Scan(&id, &dir, &data); err != nil {
			return "", err
		}
		// The id must end at the line, not merely start another agent's id.
		if !(strings.Contains(data, needle+`\n`) || strings.Contains(data, needle+`"`)) || !sameSessionWorkspace(dir, cwd) {
			continue
		}
		if found != "" && found != id {
			return "", fmt.Errorf("multiple opencode sessions match this agent; save an exact session id explicitly")
		}
		found = id
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read opencode sessions: %w", err)
	}
	return found, nil
}
