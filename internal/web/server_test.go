package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"docker-cp/internal/auth"
	"docker-cp/internal/docker"
	"docker-cp/internal/store"
)

func TestUnauthedRedirect(t *testing.T) {
	_, _, _, h := newTestServer(t)
	rec := request(t, h, "GET", "/", nil, nil, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET / = %d Location %q", rec.Code, rec.Header().Get("Location"))
	}

	req := httptest.NewRequest("GET", "/fragments/sysinfo", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("htmx: HX-Redirect = %q (status %d)", rec.Header().Get("HX-Redirect"), rec.Code)
	}

	bogus := &http.Cookie{Name: sessionCookie, Value: "nope"}
	if rec := request(t, h, "GET", "/", nil, bogus, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("bogus cookie: %d", rec.Code)
	}
}

func TestLoginSuccess(t *testing.T) {
	_, _, _, h := newTestServer(t)
	rec := request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"rahasia123"}}, nil, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, x := range rec.Result().Cookies() {
		if x.Name == "dcp_session" {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value == "" {
		t.Fatalf("cookie = %+v", c)
	}
	if c.Secure {
		t.Error("cookie Secure over plain HTTP")
	}
	dash := request(t, h, "GET", "/", nil, c, "")
	if dash.Code != http.StatusOK || !strings.Contains(dash.Body.String(), "Dashboard") {
		t.Fatalf("dashboard %d: %s", dash.Code, dash.Body.String())
	}
	if rec := request(t, h, "GET", "/login", nil, c, ""); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("GET /login while logged in = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginSecureCookieBehindHTTPS(t *testing.T) {
	_, _, _, h := newTestServer(t)
	req := httptest.NewRequest("POST", "/login", strings.NewReader("username=admin&password=rahasia123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	cs := rec.Result().Cookies()
	if len(cs) == 0 || !cs[0].Secure {
		t.Fatalf("cookies = %+v", cs)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	_, _, _, h := newTestServer(t)
	for _, f := range []url.Values{
		{"username": {"admin"}, "password": {"salah12345"}},
		{"username": {"root"}, "password": {"rahasia123"}},
	} {
		rec := request(t, h, "POST", "/login", f, nil, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Username atau password salah") {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Error("cookie set on failed login")
		}
	}
}

func TestLoginNoAdmin(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	srv.d.Store.Update(func(s *store.State) error { s.Admin = store.Admin{}; return nil })
	rec := request(t, h, "GET", "/login", nil, nil, "")
	if !strings.Contains(rec.Body.String(), "docker-cp passwd") {
		t.Fatalf("missing passwd hint: %s", rec.Body.String())
	}
	rec = request(t, h, "POST", "/login", url.Values{"username": {""}, "password": {""}}, nil, "")
	if rec.Code == http.StatusSeeOther {
		t.Fatal("login succeeded without admin")
	}
}

func TestLoginRateLimited(t *testing.T) {
	_, _, _, h := newTestServer(t)
	for i := 0; i < 5; i++ {
		request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"salah12345"}}, nil, "")
	}
	rec := request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"rahasia123"}}, nil, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt = %d", rec.Code)
	}
}

func TestCSRFRequired(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, csrf := login(t, h)
	if rec := request(t, h, "POST", "/logout", nil, c, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := request(t, h, "POST", "/logout", nil, c, "wrong"); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	rec := request(t, h, "POST", "/logout", nil, c, csrf)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("with token = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := request(t, h, "GET", "/", nil, c, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("session still valid after logout: %d", rec.Code)
	}
}

func TestCSRFFormField(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/logout", url.Values{"csrf": {csrf}}, c, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form token = %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, _, _, h := newTestServer(t)
	rec := request(t, h, "GET", "/login", nil, nil, "")
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; img-src 'self' data:",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "same-origin",
		"X-Content-Type-Options":  "nosniff",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Also on redirects produced by requireAuth.
	if got := request(t, h, "GET", "/", nil, nil, "").Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("redirect X-Frame-Options = %q", got)
	}
}

var (
	scriptTagRe = regexp.MustCompile(`<script[^>]*>`)
	inlineOnRe  = regexp.MustCompile(`\son[a-z]+=`)
)

func TestNoInlineScript(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, _ := login(t, h)
	for _, path := range []string{"/", "/login"} {
		var ck *http.Cookie
		if path == "/" {
			ck = c
		}
		body := request(t, h, "GET", path, nil, ck, "").Body.String()
		tags := scriptTagRe.FindAllString(body, -1)
		if len(tags) == 0 {
			t.Fatalf("%s: no script tags", path)
		}
		for _, tag := range tags {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s: inline script %s", path, tag)
			}
		}
		if inlineOnRe.MatchString(body) {
			t.Errorf("%s: inline event handler", path)
		}
		if !strings.Contains(body, `<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>`) {
			t.Errorf("%s: missing htmx-config meta", path)
		}
	}
}

func TestStatic(t *testing.T) {
	_, _, _, h := newTestServer(t)
	for _, p := range []string{"/static/app.js", "/static/app.css", "/static/htmx.min.js"} {
		rec := request(t, h, "GET", p, nil, nil, "")
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("%s = %d", p, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "max-age=86400" {
			t.Errorf("%s Cache-Control = %q", p, rec.Header().Get("Cache-Control"))
		}
	}
	if rec := request(t, h, "GET", "/static/", nil, nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("directory listing = %d", rec.Code)
	}
	js := request(t, h, "GET", "/static/app.js", nil, nil, "").Body.String()
	if !strings.HasPrefix(js, "// htmx 2.") {
		t.Errorf("app.js first line must record htmx version: %q", strings.SplitN(js, "\n", 2)[0])
	}
	for _, s := range []string{"htmx:configRequest", "X-CSRF-Token", "data-confirm", "streamPost", "<dialog>", "showModal"} {
		if !strings.Contains(js, s) && !(s == "<dialog>" && strings.Contains(js, `createElement("dialog")`)) {
			t.Errorf("app.js missing %q", s)
		}
	}
	if strings.Contains(js, "window.confirm") || strings.Contains(js, "alert(") {
		t.Error("app.js uses browser confirm/alert")
	}
}

func TestInsecureBanner(t *testing.T) {
	_, _, _, h := newTestServer(t)
	const banner = "tanpa HTTPS"
	if body := request(t, h, "GET", "/login", nil, nil, "").Body.String(); !strings.Contains(body, banner) {
		t.Error("no banner over plain HTTP")
	}
	req := httptest.NewRequest("GET", "/login", nil)
	req.RemoteAddr = "127.0.0.1:4000"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), banner) {
		t.Error("banner shown behind HTTPS proxy")
	}
	req = httptest.NewRequest("GET", "/login", nil)
	req.RemoteAddr = "9.9.9.9:4000"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), banner) {
		t.Error("spoofed X-Forwarded-Proto from non-loopback hid banner")
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct{ remote, xff, want string }{
		{"127.0.0.1:1", "1.2.3.4", "1.2.3.4"},
		{"127.0.0.1:1", "9.9.9.9, 1.2.3.4", "1.2.3.4"},
		{"[::1]:1", "1.2.3.4", "1.2.3.4"},
		{"127.0.0.1:1", "", "127.0.0.1"},
		{"127.0.0.1:1", "garbage", "127.0.0.1"},
		{"5.6.7.8:1", "1.2.3.4", "5.6.7.8"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%s, %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"http://evil", false},
		{"https://panel", true},
		{"http://panel", true},
		{"", false},
		{"null", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://panel/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := checkOrigin(r); got != c.want {
			t.Errorf("checkOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

func TestSysinfoFragment(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, _ := login(t, h)
	rec := request(t, h, "GET", "/fragments/sysinfo", nil, c, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "RAM") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("fragment rendered with layout")
	}
}

func TestDashboardCounts(t *testing.T) {
	srv, fake, _, h := newTestServer(t)
	fake.Handle("GET /v1.43/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Id":"1","Names":["/a"],"State":"running"},{"Id":"2","Names":["/b"],"State":"running"},{"Id":"3","Names":["/c"],"State":"exited"}]`))
	})
	srv.d.Store.Update(func(s *store.State) error {
		s.Sites = []store.Site{{ID: "s1", Domains: []string{"a.example.com"}, Target: store.Target{Host: "127.0.0.1:9000"}}}
		return nil
	})
	c, _ := login(t, h)
	body := request(t, h, "GET", "/", nil, c, "").Body.String()
	for _, s := range []string{"2 berjalan", "1 berhenti", "1 site", `hx-get="/fragments/sysinfo"`, `hx-trigger="load, every 5s"`} {
		if !strings.Contains(body, s) {
			t.Errorf("dashboard missing %q", s)
		}
	}
	if !strings.Contains(body, `href="/" aria-current="page"`) {
		t.Error("dashboard nav item not marked active")
	}

	srv.d.Docker = docker.New(filepath.Join(t.TempDir(), "none.sock"))
	rec := request(t, h, "GET", "/", nil, c, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Docker tidak terhubung") {
		t.Fatalf("docker down: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFlash(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	c, _ := login(t, h)
	set := httptest.NewRecorder()
	srv.setFlash(set, "Tersimpan & beres")
	var fl *http.Cookie
	for _, x := range set.Result().Cookies() {
		fl = x
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	req.AddCookie(fl)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Tersimpan &amp; beres") {
		t.Fatalf("flash not rendered: %s", rec.Body.String())
	}
	cleared := false
	for _, x := range rec.Result().Cookies() {
		if x.Name == flashCookie && x.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("flash cookie not cleared after render")
	}
}

func TestApplyCaddy(t *testing.T) {
	srv, _, fc, _ := newTestServer(t)
	srv.d.Store.Update(func(s *store.State) error {
		s.Sites = []store.Site{{ID: "s1", Domains: []string{"a.example.com", "b.example.com"}, Target: store.Target{Host: "127.0.0.1:9000"}}}
		return nil
	})
	if msgs := srv.applyCaddy(context.Background()); len(msgs) != 0 {
		t.Fatalf("msgs = %v", msgs)
	}
	if got := strings.Join(fc.Calls(), ","); got != "POST /adapt,POST /load" {
		t.Fatalf("calls = %s", got)
	}
	data, err := os.ReadFile(srv.d.Cfg.CaddyfilePath)
	if err != nil || !strings.Contains(string(data), "a.example.com, b.example.com {") || fc.LastLoad() != string(data) {
		t.Fatalf("Caddyfile = %q, %v", data, err)
	}

	fc.configure(func(fc *fakeCaddy) { fc.loadStatus, fc.loadBody = 400, `{"error":"bad config"}` })
	msgs := srv.applyCaddy(context.Background())
	if len(msgs) != 1 || !strings.Contains(msgs[0], "bad config") {
		t.Fatalf("msgs = %v", msgs)
	}
	if err := srv.withApplyLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLoginConcurrentRateLimited(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	var evaluated, inflight, maxInflight atomic.Int32
	srv.checkPassword = func(hash, pw string) bool {
		evaluated.Add(1)
		n := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		defer inflight.Add(-1)
		return auth.CheckPassword(hash, pw)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"salah12345"}}, nil, "")
		}()
	}
	wg.Wait()
	rec := request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"rahasia123"}}, nil, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password after flood = %d", rec.Code)
	}
	if n := evaluated.Load(); n > 5 {
		t.Errorf("%d password checks evaluated, want <= 5", n)
	}
	if m := maxInflight.Load(); m > maxLoginChecks {
		t.Errorf("%d concurrent password checks, want <= %d", m, maxLoginChecks)
	}
}

func TestLoginRotatesSession(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	old, _ := login(t, h)
	rec := request(t, h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"rahasia123"}}, old, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("relogin = %d", rec.Code)
	}
	if _, ok := srv.d.Sessions.Lookup(old.Value); ok {
		t.Error("old session still valid after re-login")
	}
}

func TestLoginBodyLimit(t *testing.T) {
	_, _, _, h := newTestServer(t)
	form := url.Values{"username": {"admin"}, "password": {"rahasia123"}, "pad": {strings.Repeat("x", 16<<10)}}
	rec := request(t, h, "POST", "/login", form, nil, "")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login body = %d", rec.Code)
	}
}

func TestCSRFNotFromQuery(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, csrf := login(t, h)
	if rec := request(t, h, "POST", "/logout?csrf="+url.QueryEscape(csrf), nil, c, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("query-string token = %d, want 403", rec.Code)
	}
}

func TestStreamJSRobust(t *testing.T) {
	_, _, _, h := newTestServer(t)
	js := request(t, h, "GET", "/static/app.js", nil, nil, "").Body.String()
	for _, s := range []string{"if (!targetEl)", "finally"} {
		if !strings.Contains(js, s) {
			t.Errorf("app.js missing %q", s)
		}
	}
}
