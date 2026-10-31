package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docker-cp/internal/compose"
)

// useFakeCompose swaps the server's compose Manager for one that runs a copy
// of testdata/fakedocker.sh and returns the path of the log it appends to.
func useFakeCompose(t *testing.T, srv *Server) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "compose", "testdata", "fakedocker.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakedocker.sh")
	if err := os.WriteFile(bin, script, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_LOG", log)
	srv.d.Compose = compose.NewManager(srv.d.Cfg.StacksDir, bin)
	return log
}

func post(t *testing.T, h http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	c, csrf := login(t, h)
	return request(t, h, "POST", target, form, c, csrf)
}

func TestStackSave(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	rec := post(t, h, "/stacks", url.Values{"name": {"shop"}, "compose": {"services: {}\n"}, "env": {"A=1\n"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(srv.d.Cfg.StacksDir, "shop", "docker-compose.yml"))
	if err != nil || string(b) != "services: {}\n" {
		t.Fatalf("compose file %q err %v", b, err)
	}
	// Existing stack: name comes from the path.
	rec = post(t, h, "/stacks/shop", url.Values{"compose": {"x: 1\n"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if b, _ := os.ReadFile(filepath.Join(srv.d.Cfg.StacksDir, "shop", "docker-compose.yml")); string(b) != "x: 1\n" {
		t.Fatalf("not overwritten: %q", b)
	}
}

func TestStackSaveBadName(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	for _, target := range []string{"/stacks", "/stacks/Bad.Name"} {
		rec := post(t, h, target, url.Values{"name": {"../x"}, "compose": {"a"}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
	}
	if _, err := os.Stat(srv.d.Cfg.StacksDir); err == nil {
		entries, _ := os.ReadDir(srv.d.Cfg.StacksDir)
		if len(entries) != 0 {
			t.Fatalf("files created: %v", entries)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(srv.d.Cfg.StacksDir), "x")); err == nil {
		t.Fatal("escaped stacks dir")
	}
}

func TestStackUpStreams(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	log := useFakeCompose(t, srv)
	if err := srv.d.Compose.Write("shop", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	rec := post(t, h, "/stacks/shop/up", url.Values{})
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "up") {
		t.Fatalf("fake docker log %q, body %q", calls, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Selesai") {
		t.Fatalf("body %q", rec.Body.String())
	}
	if !strings.Contains(strings.Join(fc.Calls(), ","), "POST /load") {
		t.Fatalf("caddy not applied: %v", fc.Calls())
	}
}

func TestStackDeployAndDelete(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	log := useFakeCompose(t, srv)
	rec := post(t, h, "/stacks/blog/deploy", url.Values{"compose": {"services: {}\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.d.Cfg.StacksDir, "blog", "docker-compose.yml")); err != nil {
		t.Fatal("deploy did not save:", err)
	}
	rec = post(t, h, "/stacks/blog/delete", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(srv.d.Cfg.StacksDir, "blog")); err == nil {
		t.Fatal("stack dir still exists")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "down") {
		t.Fatalf("log %q", calls)
	}
}

func TestStackBadAction(t *testing.T) {
	_, _, _, h := newTestServer(t)
	if rec := post(t, h, "/stacks/shop/explode", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := post(t, h, "/stacks/..%2Fx/up", url.Values{}); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestStackPagesRender(t *testing.T) {
	srv, f, _, h := newTestServer(t)
	handleList(f) // project "web" is external
	if err := srv.d.Compose.Write("shop", "services: {}\n", "K=V\n"); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/stacks").Body.String()
	for _, want := range []string{"shop", "web", "eksternal", "/projects/web/start"} {
		if !strings.Contains(body, want) {
			t.Fatalf("list missing %q: %s", want, body)
		}
	}
	body = get(t, h, "/stacks/shop").Body.String()
	if !strings.Contains(body, "services: {}") || !strings.Contains(body, "K=V") {
		t.Fatalf("editor: %s", body)
	}
	if rec := get(t, h, "/stacks/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing stack status %d", rec.Code)
	}
	if rec := get(t, h, "/stacks/new"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="name"`) {
		t.Fatalf("new: %d", rec.Code)
	}
}

func TestExternalProjectStart(t *testing.T) {
	_, f, _, h := newTestServer(t)
	handleList(f)
	f.Handle("POST /v1.43/containers/abc/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec := post(t, h, "/projects/web/start", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.Join(f.Calls(), "\n"), "POST /v1.43/containers/abc/start") {
		t.Fatalf("calls %v", f.Calls())
	}
}
