package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFeishuCredentialsStayPrivate(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "")
	t.Setenv("FEISHU_APP_SECRET", "")
	dir := t.TempDir()
	app, err := ReadFeishuApp(dir)
	if err != nil || app != (FeishuApp{}) {
		t.Fatalf("unconfigured Feishu integration: %v", err)
	}
	fixture := FeishuApp{AppID: "cli_test", AppSecret: "fixture-only"}
	if err := SaveFeishuApp(dir, fixture); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "feishu.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials were not saved with private permissions: %v", err)
	}
	app, err = ReadFeishuApp(dir)
	if err != nil || app != fixture {
		t.Fatalf("credentials could not be loaded: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "feishu.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFeishuApp(dir); err == nil {
		t.Fatal("credentials with public permissions were accepted")
	}
}
