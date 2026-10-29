package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

type localAccess struct{}

// Authenticate protects only the TCP listener; Unix socket access uses file permissions.
func Authenticate(next http.Handler, key string) http.Handler {
	expected := sha256.Sum256([]byte(key))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		valid := false
		if key != "" && len(r.Header.Values("Authorization")) == 1 && len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			provided := sha256.Sum256([]byte(parts[1]))
			valid = subtle.ConstantTimeCompare(provided[:], expected[:]) == 1
		}
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("{\"error\":\"Unauthorized\"}\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
