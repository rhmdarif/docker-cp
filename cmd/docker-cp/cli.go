package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"docker-cp/internal/auth"
	"docker-cp/internal/caddy"
	"docker-cp/internal/config"
	"docker-cp/internal/store"
)

// restartPanel restarts a running panel so it reloads state.json (the panel
// keeps state in memory and would overwrite the CLI's change on its next
// save). It reports whether a restart was attempted: only as root and only
// when systemctl exists. Tests replace it.
var restartPanel = func() (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, nil
	}
	out, err := exec.Command("systemctl", "try-restart", "docker-cp").CombinedOutput()
	if err != nil {
		return true, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// runPasswd sets the admin credentials; the password is the first line of stdin.
func runPasswd(cfg config.Config, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "admin", "admin username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setAdmin(cfg, *username, stdin, stdout, nil)
}

// runSetup is runPasswd plus the panel domain and ACME email settings. A flag
// that is not given leaves the stored value unchanged; an explicitly empty
// value clears it.
func runSetup(cfg config.Config, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "admin", "admin username")
	domain := fs.String("panel-domain", "", "panel domain")
	email := fs.String("acme-email", "", "ACME contact email")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	pd := strings.TrimSpace(*domain)
	if pd != "" {
		ds, err := caddy.ValidateDomains([]string{pd}, "", nil, "")
		if err != nil {
			return fmt.Errorf("--panel-domain: %w", err)
		}
		pd = ds[0]
	}
	em := strings.TrimSpace(*email)
	if err := caddy.ValidateEmail(em); err != nil {
		return fmt.Errorf("--acme-email: %w", err)
	}
	return setAdmin(cfg, *username, stdin, stdout, func(s *store.State) {
		if set["panel-domain"] {
			s.Settings.PanelDomain = pd
		}
		if set["acme-email"] {
			s.Settings.ACMEEmail = em
		}
	})
}

func setAdmin(cfg config.Config, username string, stdin io.Reader, stdout io.Writer, extra func(*store.State)) error {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return errors.New("no password on stdin")
	}
	pw := strings.TrimRight(line, "\r\n")
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.StatePath())
	if err != nil {
		return err
	}
	err = st.Update(func(s *store.State) error {
		s.Admin = store.Admin{Username: username, PasswordHash: hash}
		if extra != nil {
			extra(s)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "admin %q saved\n", username)
	switch tried, err := restartPanel(); {
	case err != nil:
		fmt.Fprintf(stdout, "Gagal me-restart panel (%v); jalankan: sudo systemctl restart docker-cp\n", err)
	case tried:
		fmt.Fprintln(stdout, "Panel di-restart agar perubahan berlaku")
	default:
		fmt.Fprintln(stdout, "Jika panel sedang berjalan, restart agar perubahan berlaku: sudo systemctl restart docker-cp")
	}
	return nil
}
