package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func legacyDB(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, migration := range testMigrations(t)[:version] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO teams (name, created_at, prompt) VALUES ('legacy', '2026-01-01', 'keep rules')"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO members (team, name, agent_id, agent_type, joined_at) VALUES ('legacy', 'lead', 'id', 'leader', '2026-01-01')",
		"INSERT INTO tasks (team, id, subject, status, priority, created_at, updated_at) VALUES ('legacy', 't1', 'keep task', 'pending', 'high', '2026-01-01', '2026-01-01')",
		`INSERT INTO messages (team, inbox, body, created_at) VALUES ('legacy', 'lead', '{"content":"keep message"}', '2026-01-01')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if version >= 4 {
		if _, err := db.Exec("UPDATE members SET prompt='keep member rules' WHERE team='legacy' AND name='lead'"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestMigrationUpgradesAndBacksUpLegacyData(t *testing.T) {
	for version := 1; version <= len(testMigrations(t)); version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := legacyDB(t, version)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := s.TeamPrompt(context.Background(), "legacy")
			if err != nil || prompt != "keep rules" {
				t.Fatalf("prompt=%q err=%v", prompt, err)
			}
			memberPrompt, err := s.AgentPrompt(context.Background(), "legacy", "lead", "")
			wantMemberPrompt := ""
			if version >= 4 {
				wantMemberPrompt = "keep member rules"
			}
			if err != nil || memberPrompt != wantMemberPrompt {
				t.Fatalf("migrated member prompt=%q err=%v", memberPrompt, err)
			}
			for _, table := range []string{"members", "tasks", "messages"} {
				var count int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE team='legacy'").Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(path + ".before-v*.db")
			if err != nil || len(backups) != 1 {
				t.Fatalf("backups=%v err=%v", backups, err)
			}
			db, err := sql.Open("sqlite", backups[0])
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var gotVersion int
			if err := db.QueryRow("PRAGMA user_version").Scan(&gotVersion); err != nil {
				t.Fatal(err)
			}
			if gotVersion != version {
				t.Fatalf("backup version=%d want=%d", gotVersion, version)
			}
			if err := db.QueryRow("SELECT prompt FROM teams WHERE name='legacy'").Scan(&prompt); err != nil || prompt != "keep rules" {
				t.Fatalf("backup prompt=%q err=%v", prompt, err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			backups, err = filepath.Glob(path + ".before-v*.db")
			if err != nil || len(backups) != 1 {
				t.Fatalf("reopen backups=%v err=%v", backups, err)
			}
		})
	}
}

func TestMigrationBackupFailurePreventsUpgrade(t *testing.T) {
	path := legacyDB(t, 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	if err := s.migrate(context.Background(), filepath.Join(path, "missing", "test.db"), migrationFiles); err == nil {
		t.Fatal("upgrade proceeded without backup")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version=%d after backup failure", version)
	}
}

func TestMigrationBackupIncludesCommittedWAL(t *testing.T) {
	path := legacyDB(t, 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "UPDATE teams SET prompt='rules from WAL' WHERE name='legacy'"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, err := filepath.Glob(path + ".before-v*.db")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var prompt, integrity string
	if err := backup.QueryRow("SELECT prompt FROM teams WHERE name='legacy'").Scan(&prompt); err != nil || prompt != "rules from WAL" {
		t.Fatalf("WAL backup=%q err=%v", prompt, err)
	}
	if err := backup.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
}

func TestMigrationRejectsNewerDatabase(t *testing.T) {
	path := legacyDB(t, len(testMigrations(t)))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(testMigrations(t))+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatal("newer database accepted")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatal(err)
	}
}

func TestMigrationRejectsMismatchedFrameworkVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err == nil {
		s.Close()
		t.Fatal("mismatched versions accepted")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Fatal(err)
	}
}

func TestMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	path := legacyDB(t, 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	steps := fstest.MapFS{
		"migrations/00001_initial.sql": &fstest.MapFile{Data: []byte(testMigrations(t)[0])},
		"migrations/00002_failure.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE partial (id INTEGER);\nINSERT INTO missing_table VALUES (1);\nPRAGMA user_version=2;")},
	}
	if err := s.migrate(context.Background(), path, steps); err == nil || !strings.Contains(err.Error(), "pre-upgrade backup") {
		t.Fatalf("migration error=%v", err)
	}
	var version, partial int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='partial'").Scan(&partial); err != nil {
		t.Fatal(err)
	}
	if version != 1 || partial != 0 {
		t.Fatalf("failed migration left version=%d partial=%d", version, partial)
	}
	if err := s.migrate(context.Background(), path, migrationFiles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(testMigrations(t)) {
		t.Fatalf("retry version=%d", version)
	}
}

// testMigrations reads the shipped SQL to construct databases from older releases.
func testMigrations(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var sqlFiles []string
	for _, entry := range entries {
		data, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		sqlFiles = append(sqlFiles, string(data))
	}
	return sqlFiles
}
