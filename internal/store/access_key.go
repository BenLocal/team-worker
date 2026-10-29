package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// EnsureAccessKey persists an explicit override or creates the default once.
func (s *Store) EnsureAccessKey(ctx context.Context, override string) (string, error) {
	var key string
	if strings.ContainsAny(override, " \t\r\n") {
		return "", invalidf("Access key must not contain whitespace")
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", "access_key").Scan(&key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if override != "" {
			key = override
		}
		if key == "" {
			var data [32]byte
			if _, err := rand.Read(data[:]); err != nil {
				return err
			}
			key = hex.EncodeToString(data[:])
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO settings(name, value) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value", "access_key", key)
		return err
	})
	return key, err
}

func (s *Store) AccessKey(ctx context.Context) (string, error) {
	var key string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", "access_key").Scan(&key)
	return key, err
}
