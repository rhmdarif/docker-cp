package caddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"docker-cp/internal/docker/dockertest"
	"docker-cp/internal/store"
)

type fakeCaddy struct {
	mu         sync.Mutex
	calls      []string
	loadBody   string
	loadCT     string
	loadStatus int
	adapted    string
}

func newFakeCaddy(t *testing.T, adapted string) (*fakeCaddy, *Admin) {
	t.Helper()
	fc := &fakeCaddy{loadStatus: 200, adapted: adapted}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		fc.calls = append(fc.calls, r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/adapt":
			w.Write([]byte(`{"result":` + fc.adapted + `}`))
		case "/load":
			fc.loadBody, fc.loadCT = string(b), r.Header.Get("Content-Type")
			w.WriteHeader(fc.loadStatus)
			if fc.loadStatus != 200 {
				w.Write([]byte(`{"error":"adapting config using caddyfile: unrecognized directive: foo"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return fc, NewAdmin(srv.URL)
}

func adaptedFor(hosts ...string) string {
	hs, _ := json.Marshal(hosts)
	return `{"apps":{"http":{"servers":{"srv0":{"routes":[{"match":[{"host":` + string(hs) + `}],"handle":[]}]}}}}}`
}

func testState() store.State {
	return store.State{
		Settings: store.Settings{PanelDomain: "Panel.example.com", PanelPort: 8080},
		Sites:    []store.Site{{ID: "a", Domains: []string{"a.example.com"}, Target: store.Target{Host: "127.0.0.1:9000"}}},
	}
}

func okResolve(store.Target) (string, error) { return "127.0.0.1:9000", nil }

func TestLoadContentType(t *testing.T) {
	fc, adm := newFakeCaddy(t, "{}")
	if err := adm.Load(context.Background(), "a.com {\n}\n"); err != nil {
		t.Fatal(err)
	}
	if fc.loadCT != "text/caddyfile" || fc.loadBody != "a.com {\n}\n" {
		t.Fatalf("ct=%q body=%q", fc.loadCT, fc.loadBody)
	}
}

func TestLoadError(t *testing.T) {
	fc, adm := newFakeCaddy(t, "{}")
	fc.loadStatus = 400
	err := adm.Load(context.Background(), "x")
	var ce *CaddyError
	if !errors.As(err, &ce) || ce.Message != "adapting config using caddyfile: unrecognized directive: foo" {
		t.Fatalf("err = %#v", err)
	}
}

func TestApplyWritesOnSuccess(t *testing.T) {
	fc, adm := newFakeCaddy(t, adaptedFor("panel.example.com", "a.example.com"))
	path := filepath.Join(t.TempDir(), "Caddyfile")
	ap := &Applier{Admin: adm, Path: path, Resolve: okResolve}
	if _, err := ap.Apply(context.Background(), testState()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != fc.loadBody || !strings.Contains(string(got), "a.example.com") {
		t.Fatalf("file = %q, loaded = %q", got, fc.loadBody)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if len(fc.calls) != 2 || fc.calls[0] != "/adapt" || fc.calls[1] != "/load" {
		t.Fatalf("calls = %v", fc.calls)
	}
}

func TestApplyFailureKeepsOldFile(t *testing.T) {
	fc, adm := newFakeCaddy(t, adaptedFor("panel.example.com", "a.example.com"))
	fc.loadStatus = 400
	path := filepath.Join(t.TempDir(), "Caddyfile")
	os.WriteFile(path, []byte("old"), 0o644)
	ap := &Applier{Admin: adm, Path: path, Resolve: okResolve}
	if _, err := ap.Apply(context.Background(), testState()); err == nil {
		t.Fatal("want error")
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyRejectsUnexpectedHost(t *testing.T) {
	fc, adm := newFakeCaddy(t, adaptedFor("a.example.com", "evil.com"))
	path := filepath.Join(t.TempDir(), "Caddyfile")
	os.WriteFile(path, []byte("old"), 0o644)
	ap := &Applier{Admin: adm, Path: path, Resolve: okResolve}
	_, err := ap.Apply(context.Background(), testState())
	if err == nil || !strings.Contains(err.Error(), "evil.com") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range fc.calls {
		if c == "/load" {
			t.Fatal("/load must not be called")
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyRejectsRouteWithoutMatch(t *testing.T) {
	adapted := `{"apps":{"http":{"servers":{"s":{"routes":[{"handle":[]}]}}}}}`
	fc, adm := newFakeCaddy(t, adapted)
	ap := &Applier{Admin: adm, Path: filepath.Join(t.TempDir(), "C"), Resolve: okResolve}
	if _, err := ap.Apply(context.Background(), testState()); err == nil {
		t.Fatal("want error")
	}
	if len(fc.calls) != 1 {
		t.Fatalf("calls = %v", fc.calls)
	}
}

func TestVerifyHostsEmptyHostList(t *testing.T) {
	for _, a := range []string{
		`{"apps":{"http":{"servers":{"s":{"routes":[{"match":[{"host":[]}]}]}}}}}`,
		`{"apps":{"http":{"servers":{"s":{"routes":[{"match":[{"path":["/x"]}]}]}}}}}`,
		`{"apps":{"http":{"servers":{"s":{"routes":[{"match":[]}]}}}}}`,
	} {
		if err := verifyHosts(json.RawMessage(a), []string{"a.com"}); err == nil {
			t.Errorf("want error for %s", a)
		}
	}
	if err := verifyHosts(json.RawMessage(`{}`), nil); err != nil {
		t.Errorf("no servers should pass: %v", err)
	}
}

func TestDockerResolver(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Id":"1","Names":["/klien-a"],"Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8001,"Type":"tcp"}]},{"Id":"2","Names":["/bare"],"Ports":[]}]`))
	})
	res := DockerResolver(f.Client())
	got, err := res(store.Target{Container: "klien-a", ContainerPort: 80})
	if err != nil || got != "127.0.0.1:8001" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := res(store.Target{Container: "klien-a"}); err != nil || got != "127.0.0.1:8001" {
		t.Fatalf("port 0: %q, %v", got, err)
	}
	if _, err := res(store.Target{Container: "bare"}); err == nil || !strings.Contains(err.Error(), "container tidak mem-publish port") {
		t.Fatalf("err = %v", err)
	}
	if _, err := res(store.Target{Container: "nope"}); err == nil {
		t.Fatal("want error for missing container")
	}
	if got, _ := res(store.Target{Host: "10.0.0.1:80"}); got != "10.0.0.1:80" {
		t.Fatalf("host passthrough: %q", got)
	}
}
