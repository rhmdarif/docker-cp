package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"docker-cp/internal/auth"
	"docker-cp/internal/config"
	"docker-cp/internal/store"
)

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

// runSetup is runPasswd plus the panel domain and ACME email settings.
func runSetup(cfg config.Config, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "admin", "admin username")
	domain := fs.String("panel-domain", "", "panel domain")
	email := fs.String("acme-email", "", "ACME contact email")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setAdmin(cfg, *username, stdin, stdout, func(s *store.State) {
		s.Settings.PanelDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(*domain)), ".")
		s.Settings.ACMEEmail = strings.TrimSpace(*email)
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
	return nil
}
