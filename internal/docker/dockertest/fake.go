// Package dockertest provides a fake Docker daemon on a unix socket for tests.
package dockertest

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"docker-cp/internal/docker"
)

// Fake is an in-process fake Docker Engine API server.
type Fake struct {
	sock  string
	mux   *http.ServeMux
	mu    sync.Mutex
	calls []string
}

// New starts a fake server; it is shut down via t.Cleanup.
func New(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	if len(filepath.Join(dir, "d.sock")) > 100 { // sun_path limit is ~108 bytes
		d, err := os.MkdirTemp("", "dct")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		dir = d
	}
	f := &Fake{sock: filepath.Join(dir, "d.sock"), mux: http.NewServeMux()}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		f.mux.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return f
}

// Handle registers h for a ServeMux pattern such as "POST /v1.43/containers/create".
func (f *Fake) Handle(pattern string, h http.HandlerFunc) { f.mux.HandleFunc(pattern, h) }

// Client returns a docker.Client connected to the fake.
func (f *Fake) Client() *docker.Client { return docker.New(f.sock) }

// Calls returns the recorded requests as "METHOD /path" in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
