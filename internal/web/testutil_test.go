package web

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"docker-cp/internal/auth"
	"docker-cp/internal/caddy"
	"docker-cp/internal/compose"
	"docker-cp/internal/config"
	"docker-cp/internal/docker/dockertest"
	"docker-cp/internal/store"
)

// TestMain silences the server's log output so test runs stay readable.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

const (
	testUser     = "admin"
	testPassword = "rahasia123"
)

var (
	hashOnce sync.Once
	testHash string
)

// adminHash returns a bcrypt hash of testPassword, computed once per test
// binary because cost 12 is slow.
func adminHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		h, err := auth.HashPassword(testPassword)
		if err != nil {
			panic(err)
		}
		testHash = h
	})
	return testHash
}

// fakeCaddy is a fake Caddy Admin API. By default POST /adapt answers with a
// config whose route hosts are the site addresses of the posted Caddyfile and
// POST /load answers 200. Use configure to force errors or override results.
type fakeCaddy struct {
	URL string

	mu          sync.Mutex
	calls       []string // "POST /adapt", "POST /load"
	lastLoad    string   // Caddyfile body of the last /load
	adaptStatus int      // non-zero: /adapt answers this status with adaptBody
	adaptBody   string
	adaptResult string // non-empty: replaces the computed /adapt result JSON
	loadStatus  int    // non-zero: /load answers this status with loadBody
	loadBody    string
}

func newFakeCaddy(t *testing.T) *fakeCaddy {
	t.Helper()
	fc := &fakeCaddy{}
	srv := httptest.NewServer(http.HandlerFunc(fc.serve))
	t.Cleanup(srv.Close)
	fc.URL = srv.URL
	return fc
}

func (fc *fakeCaddy) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.calls = append(fc.calls, r.Method+" "+r.URL.Path)
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/adapt":
		if fc.adaptStatus != 0 {
			w.WriteHeader(fc.adaptStatus)
			io.WriteString(w, fc.adaptBody)
			return
		}
		res := fc.adaptResult
		if res == "" {
			res = adaptedFromCaddyfile(string(body))
		}
		io.WriteString(w, `{"result":`+res+`}`)
	case "/load":
		fc.lastLoad = string(body)
		if fc.loadStatus != 0 {
			w.WriteHeader(fc.loadStatus)
			io.WriteString(w, fc.loadBody)
		}
	default:
		http.NotFound(w, r)
	}
}

// configure mutates the fake's settings under its lock.
func (fc *fakeCaddy) configure(fn func(fc *fakeCaddy)) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fn(fc)
}

func (fc *fakeCaddy) Calls() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]string(nil), fc.calls...)
}

func (fc *fakeCaddy) LastLoad() string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.lastLoad
}

// adaptedFromCaddyfile mimics Caddy's adapter just enough for the applier's
// host verification: every unindented line ending in " {" (other than the
// global options "{") is a site block whose comma-separated addresses become
// one host-matched route.
func adaptedFromCaddyfile(cf string) string {
	type match struct {
		Host []string `json:"host"`
	}
	type route struct {
		Match []match `json:"match"`
	}
	routes := []route{}
	for _, line := range strings.Split(cf, "\n") {
		if line == "" || line == "{" || line[0] == ' ' || line[0] == '\t' || !strings.HasSuffix(line, " {") {
			continue
		}
		var hosts []string
		for _, a := range strings.Split(strings.TrimSuffix(line, " {"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				hosts = append(hosts, a)
			}
		}
		routes = append(routes, route{Match: []match{{Host: hosts}}})
	}
	b, _ := json.Marshal(map[string]any{
		"apps": map[string]any{"http": map[string]any{"servers": map[string]any{"srv0": map[string]any{"routes": routes}}}},
	})
	return string(b)
}

// newTestServer builds a Server backed by a temp store (admin/rahasia123),
// a fake Docker daemon and a fake Caddy admin API.
func newTestServer(t *testing.T) (*Server, *dockertest.Fake, *fakeCaddy, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	fc := newFakeCaddy(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.StacksDir = filepath.Join(dir, "stacks")
	cfg.CaddyfilePath = filepath.Join(dir, "Caddyfile")
	cfg.CaddyAdmin = fc.URL
	cfg.DockerSock = filepath.Join(dir, "unused.sock")

	st, err := store.Open(cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	hash := adminHash(t)
	if err := st.Update(func(s *store.State) error {
		s.Admin = store.Admin{Username: testUser, PasswordHash: hash}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := dockertest.New(t)
	dc := fake.Client()
	srv, err := New(Deps{
		Cfg:      cfg,
		Store:    st,
		Sessions: auth.NewSessions(24*time.Hour, time.Now),
		Limiter:  auth.NewLimiter(5, 15*time.Minute, time.Now),
		Docker:   dc,
		Compose:  compose.NewManager(cfg.StacksDir, "docker"),
		Caddy:    &caddy.Applier{Admin: caddy.NewAdmin(fc.URL), Path: cfg.CaddyfilePath, Resolve: caddy.DockerResolver(dc)},
		Exit:     func(int) {}, // tests that exercise Exit replace srv.d.Exit
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, fake, fc, srv.Handler()
}

// request performs one request against h. A non-nil form is sent
// url-encoded; a non-empty csrf is sent as X-CSRF-Token.
func request(t *testing.T, h http.Handler, method, target string, form url.Values, c *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c != nil {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var csrfMetaRe = regexp.MustCompile(`<meta name="csrf" content="([^"]+)">`)

// login performs a real POST /login and returns the session cookie and the
// CSRF token read from the dashboard's <meta name="csrf">.
func login(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	rec := request(t, h, "POST", "/login", url.Values{"username": {testUser}, "password": {testPassword}}, nil, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie after login")
	}
	dash := request(t, h, "GET", "/", nil, cookie, "")
	m := csrfMetaRe.FindStringSubmatch(dash.Body.String())
	if dash.Code != http.StatusOK || m == nil {
		t.Fatalf("dashboard %d without csrf meta: %s", dash.Code, dash.Body.String())
	}
	return cookie, m[1]
}
