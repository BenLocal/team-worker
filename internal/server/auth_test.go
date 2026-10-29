package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, key, header string
		want              int
	}{
		{"valid", "test-only-key", "Bearer test-only-key", 200},
		{"missing", "test-only-key", "", 401},
		{"wrong", "test-only-key", "Bearer wrong-key", 401},
		{"empty server key", "", "Bearer anything", 401},
		{"wrong scheme", "test-only-key", "Basic test-only-key", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), tc.key)
			r := httptest.NewRequest(http.MethodGet, "/v1/teams", nil)
			r.Header.Set("Authorization", tc.header)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("status=%d called=%v body=%s", w.Code, called, w.Body.String())
			}
		})
	}
}
