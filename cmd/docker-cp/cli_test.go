package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"docker-cp/internal/auth"
	"docker-cp/internal/config"
	"docker-cp/internal/store"
)

func testCfg(t *testing.T) config.Config {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	return cfg
}

func TestRunPasswd(t *testing.T) {
	stubRestart(t, nil)
	cfg := testCfg(t)
	var out bytes.Buffer
	if err := runPasswd(cfg, nil, strings.NewReader("rahasia123\n"), &out); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	a := st.Get().Admin
	if a.Username != "admin" || !auth.CheckPassword(a.PasswordHash, "rahasia123") {
		t.Errorf("admin = %+v", a)
	}
}

func TestRunPasswdShort(t *testing.T) {
	cfg := testCfg(t)
	if err := runPasswd(cfg, nil, strings.NewReader("short\n"), &bytes.Buffer{}); err == nil {
		t.Error("expected error")
	}
	if err := runPasswd(cfg, nil, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("expected error on empty stdin")
	}
}

func TestRunSetup(t *testing.T) {
	stubRestart(t, nil)
	cfg := testCfg(t)
	args := []string{"--panel-domain", "Panel.Example.com.", "--acme-email", "a@b.c"}
	if err := runSetup(cfg, args, strings.NewReader("rahasia123\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(cfg.StatePath())
	s := st.Get()
	if s.Settings.PanelDomain != "panel.example.com" || s.Settings.ACMEEmail != "a@b.c" {
		t.Errorf("settings = %+v", s.Settings)
	}
	if s.Admin.Username != "admin" {
		t.Errorf("admin = %+v", s.Admin)
	}
}

// stubRestart replaces restartPanel for the test and counts calls.
func stubRestart(t *testing.T, err error) *int {
	t.Helper()
	n := 0
	old := restartPanel
	restartPanel = func() (bool, error) { n++; return true, err }
	t.Cleanup(func() { restartPanel = old })
	return &n
}

func TestSetupValidatesDomainAndEmail(t *testing.T) {
	stubRestart(t, nil)
	cfg := testCfg(t)
	for _, args := range [][]string{
		{"--panel-domain", "panel.example.com {\n}"},
		{"--panel-domain", "*.example.com"},
		{"--panel-domain", "bad domain"},
		{"--acme-email", "not-an-email"},
		{"--acme-email", "a@b.c\n}"},
	} {
		if err := runSetup(cfg, args, strings.NewReader("rahasia123\n"), &bytes.Buffer{}); err == nil {
			t.Errorf("args %q: want error", args)
		}
	}
	st, _ := store.Open(cfg.StatePath())
	if a := st.Get().Admin; a.PasswordHash != "" {
		t.Fatalf("admin written despite invalid input: %+v", a)
	}
}

func TestSetupKeepsSettingsWhenFlagsAbsent(t *testing.T) {
	stubRestart(t, nil)
	cfg := testCfg(t)
	args := []string{"--panel-domain", "panel.example.com", "--acme-email", "a@b.c"}
	if err := runSetup(cfg, args, strings.NewReader("rahasia123\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runSetup(cfg, nil, strings.NewReader("rahasia456\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(cfg.StatePath())
	s := st.Get()
	if s.Settings.PanelDomain != "panel.example.com" || s.Settings.ACMEEmail != "a@b.c" {
		t.Errorf("settings wiped: %+v", s.Settings)
	}
	if !auth.CheckPassword(s.Admin.PasswordHash, "rahasia456") {
		t.Error("password not updated")
	}
	if err := runSetup(cfg, []string{"--panel-domain", ""}, strings.NewReader("rahasia456\n"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.Open(cfg.StatePath())
	if s := st.Get().Settings; s.PanelDomain != "" || s.ACMEEmail != "a@b.c" {
		t.Errorf("explicit empty domain should clear only the domain: %+v", s)
	}
}

func TestPasswdRestartsPanel(t *testing.T) {
	n := stubRestart(t, nil)
	cfg := testCfg(t)
	var out bytes.Buffer
	if err := runPasswd(cfg, nil, strings.NewReader("rahasia123\n"), &out); err != nil {
		t.Fatal(err)
	}
	if *n != 1 || !strings.Contains(out.String(), "Panel di-restart agar perubahan berlaku") {
		t.Fatalf("restarts=%d out=%q", *n, out.String())
	}
	n = stubRestart(t, errors.New("exit status 1"))
	out.Reset()
	if err := runSetup(cfg, nil, strings.NewReader("rahasia123\n"), &out); err != nil {
		t.Fatal(err)
	}
	if *n != 1 || !strings.Contains(out.String(), "systemctl restart docker-cp") {
		t.Fatalf("restarts=%d out=%q", *n, out.String())
	}
}
