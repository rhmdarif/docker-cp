package main

import (
	"bytes"
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
