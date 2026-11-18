package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type FeishuApp struct {
	AppID     string `json:"appId"`
	AppSecret string `json:"appSecret"`
}

var feishuAppID = regexp.MustCompile(`^cli_[A-Za-z0-9]+$`)

func (a FeishuApp) Validate() error {
	if !feishuAppID.MatchString(a.AppID) || a.AppSecret == "" || len(a.AppSecret) > 4096 {
		return errors.New("A Feishu App ID and App Secret are required")
	}
	return nil
}

func FeishuManagedByEnvironment() bool {
	return os.Getenv("FEISHU_APP_ID") != "" || os.Getenv("FEISHU_APP_SECRET") != ""
}

func ReadFeishuApp(dataDir string) (FeishuApp, error) {
	a := FeishuApp{AppID: os.Getenv("FEISHU_APP_ID"), AppSecret: os.Getenv("FEISHU_APP_SECRET")}
	if a.AppID != "" || a.AppSecret != "" {
		return a, a.Validate()
	}
	file, err := os.Open(filepath.Join(dataDir, "feishu.json"))
	if errors.Is(err, os.ErrNotExist) {
		return FeishuApp{}, nil
	}
	if err != nil {
		return FeishuApp{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return FeishuApp{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8192 {
		return FeishuApp{}, errors.New("Feishu credentials must be in a private regular file")
	}
	if err := json.NewDecoder(file).Decode(&a); err != nil {
		return FeishuApp{}, errors.New("Invalid Feishu credentials file")
	}
	return a, a.Validate()
}

func SaveFeishuApp(dataDir string, app FeishuApp) error {
	if err := app.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dataDir, ".feishu-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(app)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write Feishu credentials: %w", err)
	}
	return os.Rename(file.Name(), filepath.Join(dataDir, "feishu.json"))
}
