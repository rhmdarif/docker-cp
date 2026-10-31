package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"docker-cp/internal/store"
)

func siteForm(domains string) url.Values {
	return url.Values{
		"domains":        {domains},
		"target_type":    {"container"},
		"container":      {"klien-a"},
		"container_port": {"80"},
	}
}

func hasCall(calls []string, c string) bool { return slices.Contains(calls, c) }

func TestCreateSiteAppliesThenPersists(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	handleList(f)
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/sites", siteForm("a.klien.com"), c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	load := fc.LastLoad()
	if !strings.Contains(load, "a.klien.com {") || !strings.Contains(load, "reverse_proxy 127.0.0.1:8001") {
		t.Fatalf("load body:\n%s", load)
	}
	sites := srv.d.Store.Get().Sites
	if len(sites) != 1 || sites[0].Domains[0] != "a.klien.com" || sites[0].Target.Container != "klien-a" || sites[0].Target.ContainerPort != 80 {
		t.Fatalf("sites = %+v", sites)
	}
	if _, err := os.Stat(srv.d.Cfg.CaddyfilePath); err != nil {
		t.Fatalf("Caddyfile not written: %v", err)
	}
}

func TestCreateSiteRejectedNotPersisted(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	handleList(f)
	fc.configure(func(fc *fakeCaddy) {
		fc.loadStatus = 400
		fc.loadBody = `{"error":"unrecognized directive: foo"}`
	})
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/sites", siteForm("a.klien.com"), c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unrecognized directive: foo") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "a.klien.com") {
		t.Fatal("form did not keep user input")
	}
	if n := len(srv.d.Store.Get().Sites); n != 0 {
		t.Fatalf("%d sites persisted", n)
	}
	if _, err := os.Stat(srv.d.Cfg.CaddyfilePath); !os.IsNotExist(err) {
		t.Fatalf("Caddyfile written: %v", err)
	}
}

func TestCreateSiteBraceInjection(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	handleList(f)
	c, csrf := login(t, h)
	form := siteForm("a.klien.com")
	form.Set("custom", "}\r\nevil.com {")
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if hasCall(fc.Calls(), "POST /load") {
		t.Fatal("/load called")
	}
	if len(srv.d.Store.Get().Sites) != 0 {
		t.Fatal("site persisted")
	}
}

func TestCreateSiteDuplicateDomain(t *testing.T) {
	srv, f, _, h := newTestServer(t)
	handleList(f)
	c, csrf := login(t, h)
	if rec := request(t, h, "POST", "/sites", siteForm("a.klien.com"), c, csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := request(t, h, "POST", "/sites", siteForm("A.klien.com"), c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "sudah dipakai") {
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(srv.d.Store.Get().Sites); n != 1 {
		t.Fatalf("%d sites", n)
	}
}

func TestCreateSiteInvalidHostTarget(t *testing.T) {
	_, _, fc, h := newTestServer(t)
	c, csrf := login(t, h)
	form := url.Values{"domains": {"a.klien.com"}, "target_type": {"host"}, "host": {"127.0.0.1:80 {"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fc.Calls()) != 0 {
		t.Fatalf("caddy calls %v", fc.Calls())
	}
}

func TestManualCatchAllRejected(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	fc.configure(func(fc *fakeCaddy) {
		fc.adaptResult = `{"apps":{"http":{"servers":{"srv0":{"routes":[{"handle":[{"handler":"static_response"}]}]}}}}}`
	})
	c, csrf := login(t, h)
	form := url.Values{"manual": {"on"}, "manual_block": {":80 {\n\trespond hi\n}"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "catch-all") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if hasCall(fc.Calls(), "POST /load") || len(srv.d.Store.Get().Sites) != 0 {
		t.Fatal("manual catch-all was applied or persisted")
	}
}

func TestManualSiteStoresAdaptedHosts(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	c, csrf := login(t, h)
	form := url.Values{"manual": {"on"}, "manual_block": {"M.klien.com, n.klien.com {\r\n\trespond hi\r\n}"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sites := srv.d.Store.Get().Sites
	if len(sites) != 1 || !sites[0].Manual || !slices.Equal(sites[0].Domains, []string{"m.klien.com", "n.klien.com"}) {
		t.Fatalf("sites = %+v", sites)
	}
	if strings.Contains(sites[0].ManualBlock, "\r") {
		t.Fatal("CR stored")
	}
	if !strings.Contains(fc.LastLoad(), "respond hi") {
		t.Fatalf("load:\n%s", fc.LastLoad())
	}
}

func TestManualDomainCollidesWithPanel(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	srv.d.Store.Update(func(s *store.State) error { s.Settings.PanelDomain = "panel.klien.com"; return nil })
	c, csrf := login(t, h)
	form := url.Values{"manual": {"on"}, "manual_block": {"panel.klien.com {\n\trespond hi\n}"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || hasCall(fc.Calls(), "POST /load") {
		t.Fatalf("status %d calls %v", rec.Code, fc.Calls())
	}
}

func seedSite(t *testing.T, srv *Server, site store.Site) {
	t.Helper()
	if err := srv.d.Store.Update(func(s *store.State) error { s.Sites = append(s.Sites, site); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPanelRowNotDeletable(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	srv.d.Store.Update(func(s *store.State) error { s.Settings.PanelDomain = "panel.klien.com"; return nil })
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Host: "127.0.0.1:9000"}, Custom: "encode gzip"})
	rec := get(t, h, "/sites")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "panel.klien.com") || !strings.Contains(body, "a.klien.com") || !strings.Contains(body, "custom") {
		t.Fatalf("status %d: %s", rec.Code, body)
	}
	if strings.Count(body, `/delete"`) != 1 || !strings.Contains(body, `action="/sites/s1/delete"`) {
		t.Fatalf("expected exactly one delete form (site s1): %s", body)
	}
}

func TestEditSiteKeepsOwnDomains(t *testing.T) {
	srv, _, _, h := newTestServer(t)
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Host: "127.0.0.1:9000"}})
	c, csrf := login(t, h)
	if rec := request(t, h, "GET", "/sites/s1", nil, c, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "127.0.0.1:9000") {
		t.Fatalf("edit page %d", rec.Code)
	}
	form := url.Values{"domains": {"a.klien.com\nb.klien.com"}, "target_type": {"host"}, "host": {"127.0.0.1:9001"}}
	rec := request(t, h, "POST", "/sites/s1", form, c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sites := srv.d.Store.Get().Sites
	if len(sites) != 1 || len(sites[0].Domains) != 2 || sites[0].Target.Host != "127.0.0.1:9001" {
		t.Fatalf("sites = %+v", sites)
	}
	if rec := request(t, h, "POST", "/sites/nope", form, c, csrf); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}

func TestDeleteSite(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Host: "127.0.0.1:9000"}})
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/sites/s1/delete", url.Values{}, c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(srv.d.Store.Get().Sites) != 0 || strings.Contains(fc.LastLoad(), "a.klien.com") || !hasCall(fc.Calls(), "POST /load") {
		t.Fatalf("not deleted; load:\n%s", fc.LastLoad())
	}
}

func TestDeleteSiteRejectedKeepsSite(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Host: "127.0.0.1:9000"}})
	fc.configure(func(fc *fakeCaddy) { fc.loadStatus = 400; fc.loadBody = `{"error":"boom"}` })
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/sites/s1/delete", url.Values{}, c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(srv.d.Store.Get().Sites) != 1 {
		t.Fatal("site removed despite rejected apply")
	}
}

func TestSiteFormWithoutDocker(t *testing.T) {
	_, _, _, h := newTestServer(t) // no containers/json handler: Docker errors
	rec := get(t, h, "/sites/new")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `name="host"`) || !strings.Contains(body, "Docker tidak terhubung") {
		t.Fatalf("status %d: %s", rec.Code, body)
	}
	if strings.Contains(body, `value="container"`) {
		t.Fatal("container option offered without Docker")
	}
}

func TestCreateSiteHostInjectionRejected(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	c, csrf := login(t, h)
	for _, host := range []string{
		"[fe80::1%x}\nevil.com {\n\trespond pwn]:80",
		"[fe80::1%eth0]:80",
		"127.0.0.1:80\nroot * /",
		"127.0.0.1 :80",
		"127.0.0.1:80}",
		"{$H}:80",
	} {
		form := url.Values{"domains": {"a.klien.com"}, "target_type": {"host"}, "host": {host}}
		if rec := request(t, h, "POST", "/sites", form, c, csrf); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: status %d", host, rec.Code)
		}
	}
	if len(fc.Calls()) != 0 || len(srv.d.Store.Get().Sites) != 0 {
		t.Fatalf("calls %v sites %v", fc.Calls(), srv.d.Store.Get().Sites)
	}
}

func TestEditContainerSiteWithoutDocker(t *testing.T) {
	srv, _, _, h := newTestServer(t) // Docker unreachable
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Container: "klien-a", ContainerPort: 80}})
	body := get(t, h, "/sites/s1").Body.String()
	if !strings.Contains(body, `value="container" checked`) || !strings.Contains(body, `<option value="klien-a" selected>`) ||
		!strings.Contains(body, "Docker tidak terhubung") {
		t.Fatalf("stored container target not kept: %s", body)
	}
}

// failingWriteServer makes Applier's Caddyfile write fail after /load.
func failingWriteServer(t *testing.T, srv *Server) {
	t.Helper()
	srv.d.Caddy.Path = filepath.Join(t.TempDir(), "missing-dir", "Caddyfile")
}

func TestCreateSiteWriteFailureRollsBack(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	failingWriteServer(t, srv)
	c, csrf := login(t, h)
	form := url.Values{"domains": {"a.klien.com"}, "target_type": {"host"}, "host": {"127.0.0.1:9000"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "dipulihkan") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	loads := 0
	for _, c := range fc.Calls() {
		if c == "POST /load" {
			loads++
		}
	}
	if loads != 2 || strings.Contains(fc.LastLoad(), "a.klien.com") {
		t.Fatalf("expected rollback load of previous config; loads=%d last:\n%s", loads, fc.LastLoad())
	}
	if len(srv.d.Store.Get().Sites) != 0 {
		t.Fatal("persisted")
	}
}

func TestCreateSiteWriteFailureRollbackFails(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	failingWriteServer(t, srv)
	fc.configure(func(fc *fakeCaddy) { fc.loadOK = 1; fc.loadStatus = 500; fc.loadBody = `{"error":"down"}` })
	c, csrf := login(t, h)
	form := url.Values{"domains": {"a.klien.com"}, "target_type": {"host"}, "host": {"127.0.0.1:9000"}}
	rec := request(t, h, "POST", "/sites", form, c, csrf)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "belum tersimpan") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(srv.d.Store.Get().Sites) != 0 {
		t.Fatal("persisted")
	}
}
