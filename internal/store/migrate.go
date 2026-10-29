package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate backs up existing databases, adopts legacy versions, then runs Goose.
func (s *Store) migrate(ctx context.Context, path string, files fs.FS) (err error) {
	files, err = fs.Sub(files, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	sources := provider.ListSources()
	target := sources[len(sources)-1].Version
	var version int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	var hasHistory bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", goose.DefaultTablename).Scan(&hasHistory); err != nil {
		return err
	}
	history, err := database.NewStore(goose.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return err
	}
	if hasHistory {
		current, err := history.GetLatestVersion(ctx, s.db)
		if err != nil {
			return err
		}
		if current != version {
			return fmt.Errorf("migration version mismatch: Goose=%d user_version=%d", current, version)
		}
	}
	if version > target {
		return fmt.Errorf("database version %d is newer than supported version %d", version, target)
	}
	if version < 0 {
		return fmt.Errorf("invalid database version %d", version)
	}
	if version > 0 && (version < target || !hasHistory) {
		backup, backupErr := s.backupMigration(ctx, path, version)
		if backupErr != nil {
			return backupErr
		}
		defer func() {
			if err != nil {
				err = fmt.Errorf("%w (pre-upgrade backup: %s)", err, backup)
			}
		}()
	}
	if version > 0 && !hasHistory {
		// Adopt already applied SQL without replaying it on legacy databases.
		if err := s.write(ctx, func(tx *sql.Tx) error {
			if err := history.CreateVersionTable(ctx, tx); err != nil {
				return err
			}
			if err := history.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
				return err
			}
			for _, source := range sources {
				if source.Version > version {
					break
				}
				if err := history.Insert(ctx, tx, database.InsertRequest{Version: source.Version}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("adopt legacy database version %d: %w", version, err)
		}
	}
	_, err = provider.Up(ctx)
	return err
}

func (s *Store) backupMigration(ctx context.Context, path string, version int64) (string, error) {
	backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+fmt.Sprintf(".before-v%d-*.db", version))
	if err != nil {
		return "", fmt.Errorf("create migration backup: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return "", fmt.Errorf("close migration backup: %w", err)
	}
	// VACUUM INTO includes committed WAL data in a standalone snapshot.
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		if removeErr := os.Remove(backupPath); removeErr != nil {
			return "", errors.Join(fmt.Errorf("back up database: %w", err), removeErr)
		}
		return "", fmt.Errorf("back up database: %w", err)
	}
	return backupPath, nil
}
