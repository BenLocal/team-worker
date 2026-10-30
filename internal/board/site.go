package board

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/benlocal/team-worker/internal/client"
)

// Link is a link to another service in the page header.
type Link struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ParseLinks reads NAME=URL pairs, as given to --link.
func ParseLinks(pairs []string) ([]Link, error) {
	links := []Link{}
	for _, p := range pairs {
		name, url, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("invalid board link %q: use NAME=URL", p)
		}
		links = append(links, Link{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	return links, nil
}

// Site is the whole web board behind the access key: the page, its API,
// live terminals and the header links. basePath is the public path prefix a
// reverse proxy serves it under, or "" at the root.
func Site(c *client.Client, key, basePath string, links []Link) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/links", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(links)
	})
	mux.Handle("/", Handler(c))
	return Protect(mux, key, basePath)
}
