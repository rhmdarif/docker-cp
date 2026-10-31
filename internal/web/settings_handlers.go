package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docker-cp/internal/auth"
	"docker-cp/internal/caddy"
	"docker-cp/internal/ports"
	"docker-cp/internal/store"
)

// restartDelay is how long after answering a port change the panel exits so
// systemd restarts it on the new port.
const restartDelay = time.Second

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", s.settingsPage)
	mux.HandleFunc("POST /settings", s.settingsSave)
	mux.HandleFunc("POST /settings/password", s.passwordChange)
}

type settingsData struct {
	PanelDomain, PanelPort, ACMEEmail string
	Error                             string
	PasswordError                     string
	RestartPort                       int // non-zero: the panel is restarting on this port
}

func settingsFrom(st store.Settings) settingsData {
	return settingsData{PanelDomain: st.PanelDomain, PanelPort: strconv.Itoa(st.PanelPort), ACMEEmail: st.ACMEEmail}
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings", settingsFrom(s.d.Store.Get().Settings))
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSiteBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Permintaan terlalu besar atau tidak valid.", http.StatusRequestEntityTooLarge)
		return
	}
	data := settingsData{
		PanelDomain: strings.TrimSpace(r.PostFormValue("panel_domain")),
		PanelPort:   strings.TrimSpace(r.PostFormValue("panel_port")),
		ACMEEmail:   strings.TrimSpace(r.PostFormValue("acme_email")),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyTimeout)
	defer cancel()

	var oldPort, newPort int
	err := s.withApplyLock(func() error {
		st := s.d.Store.Get()
		next, err := s.validateSettings(ctx, data, st)
		if err != nil {
			return err
		}
		oldPort, newPort = st.Settings.PanelPort, next.PanelPort
		candidate := st
		candidate.Settings = next
		_, err = s.commit(ctx, st, candidate, func(n *store.State) { n.Settings = next })
		return err
	})
	if err != nil {
		var fe *formError
		if !errors.As(err, &fe) {
			fe = &formError{http.StatusInternalServerError, err.Error()}
		}
		data.Error = fe.msg
		s.renderStatus(w, r, fe.status, "settings", data)
		return
	}
	if newPort != oldPort {
		data = settingsFrom(s.d.Store.Get().Settings)
		data.RestartPort = newPort
		s.render(w, r, "settings", data)
		time.AfterFunc(restartDelay, func() { s.d.Exit(0) })
		return
	}
	s.setFlash(w, "Pengaturan disimpan.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// validateSettings checks the form against st and returns the new settings.
func (s *Server) validateSettings(ctx context.Context, f settingsData, st store.State) (store.Settings, error) {
	next := st.Settings
	next.PanelDomain = ""
	if f.PanelDomain != "" {
		ds, err := caddy.ValidateDomains([]string{f.PanelDomain}, "", st.Sites, "")
		if err != nil {
			return next, invalid(fmt.Errorf("domain panel: %w", err))
		}
		next.PanelDomain = ds[0]
	}
	if err := caddy.ValidateEmail(f.ACMEEmail); err != nil {
		return next, invalid(err)
	}
	next.ACMEEmail = f.ACMEEmail
	port, err := strconv.Atoi(f.PanelPort)
	if err != nil || port < 1024 || port > 65535 {
		return next, invalid(errors.New("port panel harus 1024-65535"))
	}
	if port != st.Settings.PanelPort {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cs, err := s.d.Docker.ListContainers(lctx)
		if err != nil {
			return next, &formError{http.StatusBadGateway, "Tidak bisa memeriksa port: Docker tidak terhubung: " + err.Error()}
		}
		if ports.UsedPorts(cs)[port] {
			return next, invalid(fmt.Errorf("port %d sudah dipakai container", port))
		}
	}
	next.PanelPort = port
	return next, nil
}

func (s *Server) passwordChange(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Permintaan terlalu besar atau tidak valid.", http.StatusRequestEntityTooLarge)
		return
	}
	cur, nw, confirm := r.PostFormValue("current"), r.PostFormValue("new"), r.PostFormValue("confirm")
	fail := func(msg string) {
		data := settingsFrom(s.d.Store.Get().Settings)
		data.PasswordError = msg
		s.renderStatus(w, r, http.StatusUnprocessableEntity, "settings", data)
	}
	select {
	case s.loginSem <- struct{}{}:
		defer func() { <-s.loginSem }()
	case <-r.Context().Done():
		return
	}
	if !s.checkPassword(s.d.Store.Get().Admin.PasswordHash, cur) {
		fail("Password lama salah.")
		return
	}
	if nw != confirm {
		fail("Password baru dan konfirmasi tidak sama.")
		return
	}
	hash, err := auth.HashPassword(nw)
	if errors.Is(err, auth.ErrPasswordTooShort) {
		fail("Password baru minimal 8 karakter.")
		return
	}
	if err != nil {
		fail("Gagal membuat hash password: " + err.Error())
		return
	}
	if err := s.d.Store.Update(func(st *store.State) error { st.Admin.PasswordHash = hash; return nil }); err != nil {
		data := settingsFrom(s.d.Store.Get().Settings)
		data.PasswordError = "Gagal menyimpan password: " + err.Error()
		s.renderStatus(w, r, http.StatusInternalServerError, "settings", data)
		return
	}
	s.d.Sessions.DeleteAll()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	s.setFlash(w, "Password diganti. Silakan login dengan password baru.")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
