package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/config"
)

func TestFeishuAppCredentials(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "")
	t.Setenv("FEISHU_APP_SECRET", "")
	dir := t.TempDir()
	s := &Server{runDataDir: dir}
	const path = "/v1/chat/feishu/app"
	const body = `{"appId":"cli_example","appSecret":"example-only"}`
	handler := http.HandlerFunc(s.feishuApp)
	remote := httptest.NewRecorder()
	handler.ServeHTTP(remote, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
	if remote.Code != http.StatusForbidden {
		t.Fatalf("remote PUT status = %d, want 403", remote.Code)
	}

	local := localTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if strings.Contains(rec.Body.String(), "appSecret") || strings.Contains(rec.Body.String(), "example-only") {
			t.Error("credential response included the secret")
		}
		w.WriteHeader(rec.Code)
	})}
	request := func(method string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(t.Context())
		res, err := local.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("local %s status = %d, want %d", method, res.StatusCode, want)
		}
	}
	request(http.MethodPut, http.StatusOK)
	request(http.MethodGet, http.StatusOK)
	info, err := os.Stat(filepath.Join(dir, "feishu.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file has permissions %o", info.Mode().Perm())
	}
	app, err := config.ReadFeishuApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	if app.AppID != "cli_example" || app.AppSecret != "example-only" {
		t.Fatal("stored credentials did not match the submission")
	}
	t.Setenv("FEISHU_APP_ID", "cli_managed")
	t.Setenv("FEISHU_APP_SECRET", "environment-example")
	request(http.MethodPut, http.StatusConflict)
	request(http.MethodGet, http.StatusOK)
}
