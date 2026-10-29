package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredRemoteClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-key" || r.URL.Path != "/v1/teams" {
			t.Errorf("unexpected remote request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()
	t.Setenv("TEAM_WORKER_SERVER_URL", ts.URL+"/")
	t.Setenv("TEAM_WORKER_API_KEY", "test-only-key")
	c := New(t.TempDir())
	if c.start != nil {
		t.Fatal("remote client must not autostart a local daemon")
	}
	if _, err := c.ListTeams(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteURLValidation(t *testing.T) {
	for _, address := range []string{"file:///tmp/api", "http://", "http://user:secret@localhost", "http://localhost?key=secret", "http://localhost/#fragment"} {
		if _, err := NewRemote(address, "test-only-key"); err == nil {
			t.Fatalf("accepted invalid URL %q", address)
		}
	}
}

func TestRemoteDoesNotForwardKeyOnRedirect(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	c, err := NewRemote(origin.URL, "test-only-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTeams(context.Background()); err == nil || called {
		t.Fatalf("redirect followed: error=%v called=%v", err, called)
	}
}
