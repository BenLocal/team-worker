package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/store"
)

func TestConductNeedsARunningRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.CreateTeam(context.Background(), store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)
	defer srv.Close()
	call := func(method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, "/v1/teams/demo/harness/conduct", strings.NewReader("{}")))
		return rec
	}
	if rec := call(http.MethodPost); rec.Code != http.StatusNotFound {
		t.Fatalf("no run to conduct: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodDelete); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stopped":false`) {
		t.Fatalf("nothing to stop: %d %s", rec.Code, rec.Body)
	}
}
