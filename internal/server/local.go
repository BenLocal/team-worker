package server

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/benlocal/team-worker/internal/client"
)

// localTransport answers a client's requests by calling the API handler in
// this process, with the same rights as a client on the Unix socket. The
// board inside the daemon uses it, so it reads the API without a network
// hop. It buffers whole responses, so it does not suit long polls.
type localTransport struct{ handler http.Handler }

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), localAccess{}, true)))
	res := rec.Result()
	res.Request = r
	return res, nil
}

// LocalClient is an API client served by handler in this process.
func LocalClient(handler http.Handler) *client.Client {
	return client.NewHTTP("http://team-workerd", &http.Client{Transport: localTransport{handler}})
}
