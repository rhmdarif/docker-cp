package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"docker-cp/internal/store"
)

func settingsForm(domain, port, email string) url.Values {
	return url.Values{"panel_domain": {domain}, "panel_port": {port}, "acme_email": {email}}
}

func TestSettingsPortChangeExits(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	srv.listening = noneListening
	handleList(f)
	exited := make(chan int, 1)
	srv.d.Exit = func(code int) { exited <- code }
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/settings", settingsForm("", "8900", ""), c, csrf)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Panel restart ke port 8900") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p := srv.d.Store.Get().Settings.PanelPort; p != 8900 {
		t.Fatalf("PanelPort = %d", p)
	}
	if !hasCall(fc.Calls(), "POST /load") {
		t.Fatal("settings not applied to Caddy")
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Exit not called")
	}
}

func TestSettingsSameValuesNoExit(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	srv.d.Exit = func(int) { t.Error("Exit called") }
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/settings", settingsForm("Panel.Klien.com.", "8800", "ops@klien.com"), c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	st := srv.d.Store.Get().Settings
	if st.PanelDomain != "panel.klien.com" || st.ACMEEmail != "ops@klien.com" {
		t.Fatalf("settings = %+v", st)
	}
	if !strings.Contains(fc.LastLoad(), "panel.klien.com {") || !strings.Contains(fc.LastLoad(), "email ops@klien.com") {
		t.Fatalf("load:\n%s", fc.LastLoad())
	}
	time.Sleep(1200 * time.Millisecond)
}

func TestSettingsValidation(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	handleList(f) // klien-a publishes 8001
	seedSite(t, srv, store.Site{ID: "s1", Domains: []string{"a.klien.com"}, Target: store.Target{Host: "127.0.0.1:9000"}})
	c, csrf := login(t, h)
	for _, tc := range []struct{ name, domain, port, email string }{
		{"port low", "", "80", ""},
		{"port high", "", "70000", ""},
		{"port nan", "", "abc", ""},
		{"port used", "", "8001", ""},
		{"domain site", "a.klien.com", "8800", ""},
		{"domain bad", "a b", "8800", ""},
		{"email bad", "", "8800", "a b@c"},
	} {
		rec := request(t, h, "POST", "/settings", settingsForm(tc.domain, tc.port, tc.email), c, csrf)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d", tc.name, rec.Code)
		}
	}
	if hasCall(fc.Calls(), "POST /load") {
		t.Fatal("/load called on invalid settings")
	}
	if st := srv.d.Store.Get().Settings; st.PanelPort != 8800 || st.PanelDomain != "" {
		t.Fatalf("settings changed: %+v", st)
	}
}

func TestSettingsCaddyRejectedNotPersisted(t *testing.T) {
	srv, _, fc, h := newTestServer(t)
	fc.configure(func(fc *fakeCaddy) { fc.loadStatus = 400; fc.loadBody = `{"error":"bad email"}` })
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/settings", settingsForm("", "8800", "ops@klien.com"), c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "bad email") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if srv.d.Store.Get().Settings.ACMEEmail != "" {
		t.Fatal("persisted")
	}
}

func TestPasswordChange(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, csrf := login(t, h)
	pw := func(cur, nw, conf string) int {
		return request(t, h, "POST", "/settings/password", url.Values{"current": {cur}, "new": {nw}, "confirm": {conf}}, c, csrf).Code
	}
	if code := pw("salah12345", "barubaru1", "barubaru1"); code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current: %d", code)
	}
	if code := pw(testPassword, "barubaru1", "barubaru2"); code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", code)
	}
	if code := pw(testPassword, "pendek", "pendek"); code != http.StatusUnprocessableEntity {
		t.Fatalf("too short: %d", code)
	}
	rec := request(t, h, "POST", "/settings/password", url.Values{"current": {testPassword}, "new": {"barubaru1"}, "confirm": {"barubaru1"}}, c, csrf)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("success: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := request(t, h, "GET", "/", nil, c, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("old session still valid: %d", rec.Code)
	}
	if rec := request(t, h, "POST", "/login", url.Values{"username": {testUser}, "password": {testPassword}}, nil, ""); rec.Code == http.StatusSeeOther {
		t.Fatal("old password still works")
	}
	if rec := request(t, h, "POST", "/login", url.Values{"username": {testUser}, "password": {"barubaru1"}}, nil, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("new password login: %d", rec.Code)
	}
}

func TestSettingsRejectsListeningPort(t *testing.T) {
	srv, f, _, h := newTestServer(t)
	handleList(f)
	srv.listening = func(p int) bool { return p == 8900 || p == 8800 }
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/settings", settingsForm("", "8900", ""), c, csrf)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "port 8900 sedang dipakai") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p := srv.d.Store.Get().Settings.PanelPort; p != 8800 {
		t.Fatalf("PanelPort = %d", p)
	}
	// The panel's own (listening) port stays acceptable.
	rec = request(t, h, "POST", "/settings", settingsForm("", "8800", ""), c, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("own port: status %d: %s", rec.Code, rec.Body.String())
	}
	if body := get(t, h, "/settings").Body.String(); !strings.Contains(body, "ufw") {
		t.Fatalf("no ufw note:\n%s", body)
	}
}
