package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/benlocal/team-worker/internal/model"
)

func validateChatAssets(assets []model.ChatAsset) error {
	if len(assets) > model.ChatAssetCount {
		return invalidf("Include at most %d images, files and links per result", model.ChatAssetCount)
	}
	total := 0
	for _, asset := range assets {
		if strings.TrimSpace(asset.Name) == "" || len(asset.Name) > 255 ||
			!utf8.ValidString(asset.Name) || strings.ContainsFunc(asset.Name, unicode.IsControl) {
			return invalidf("An attachment name or document title must be between 1 and 255 bytes without control characters")
		}
		if asset.Kind == model.ChatAssetLink {
			link, err := url.Parse(asset.URL)
			if err != nil || len(asset.URL) > 2048 || strings.ContainsAny(asset.URL, "\\<>") ||
				strings.ContainsFunc(asset.URL, unicode.IsSpace) || link.Scheme != "https" ||
				link.Hostname() == "" || link.User != nil || len(asset.Data) != 0 {
				return invalidf("A cloud document needs an HTTPS link without credentials, up to 2048 bytes")
			}
			continue
		}
		limit := model.ChatImageLimit
		switch asset.Kind {
		case model.ChatAssetFile:
			limit = model.ChatFileLimit
		case model.ChatAssetPreview, model.ChatAssetImage:
		default:
			return invalidf("Unsupported attachment kind")
		}
		if asset.URL != "" || strings.ContainsAny(asset.Name, "/\\") || asset.Name == "." || asset.Name == ".." {
			return invalidf("Upload an attachment with a file name and no URL")
		}
		if len(asset.Data) == 0 || len(asset.Data) > limit {
			return invalidf("Each attachment must be between 1 and %d bytes", limit)
		}
		total += len(asset.Data)
		if total > model.ChatFileLimit {
			return invalidf("The combined attachment size must be at most %d bytes", model.ChatFileLimit)
		}
	}
	return nil
}

func chatResultJobID(id, kind, material string, assets []model.ChatAsset) string {
	hash := sha256.New()
	if len(assets) != 0 {
		hash.Write([]byte("assets\x00"))
	}
	hash.Write([]byte(id + "\x00" + kind + "\x00" + material))
	for _, asset := range assets {
		fmt.Fprintf(hash, "\x00%q %q %q %x", asset.Kind, asset.Name, asset.URL, sha256.Sum256(asset.Data))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func insertChatAssets(ctx context.Context, tx *sql.Tx, jobID string, assets []model.ChatAsset) error {
	if len(assets) == 0 {
		return nil
	}
	values := make([]string, len(assets))
	args := make([]any, 0, 6*len(assets))
	for i, asset := range assets {
		values[i] = "(?,?,?,?,?,?)"
		args = append(args, jobID, i, asset.Kind, asset.Name, asset.URL, asset.Data)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO chat_result_assets(job_id,position,kind,name,url,data) VALUES `+strings.Join(values, ","), args...)
	return err
}

func (s *Store) ChatReplyAssets(ctx context.Context, jobID string) ([]model.ChatAsset, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT kind,name,url,data FROM chat_result_assets WHERE job_id=? ORDER BY position", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []model.ChatAsset
	for rows.Next() {
		var asset model.ChatAsset
		if err := rows.Scan(&asset.Kind, &asset.Name, &asset.URL, &asset.Data); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}
