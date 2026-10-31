package web

import (
	"crypto/subtle"
	"net/http"

	"docker-cp/internal/auth"
)

type loginData struct {
	Username string
	Error    string
	NoAdmin  bool
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupSession(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", loginData{NoAdmin: s.d.Store.Get().Admin.PasswordHash == ""})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	username, password := r.PostFormValue("username"), r.PostFormValue("password")
	data := loginData{Username: username}
	if !s.d.Limiter.Allow(ip) {
		data.Error = "Terlalu banyak percobaan gagal. Coba lagi dalam 15 menit."
		s.renderStatus(w, r, http.StatusTooManyRequests, "login", data)
		return
	}
	admin := s.d.Store.Get().Admin
	if admin.PasswordHash == "" {
		data.NoAdmin = true
		s.render(w, r, "login", data)
		return
	}
	// Always run bcrypt so a wrong username costs the same as a wrong password.
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(admin.Username)) == 1
	passOK := auth.CheckPassword(admin.PasswordHash, password)
	if !userOK || !passOK {
		s.d.Limiter.Fail(ip)
		data.Error = "Username atau password salah"
		s.render(w, r, "login", data)
		return
	}
	s.d.Limiter.Reset(ip)
	sess := s.d.Sessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sess.Token, Path: "/", Expires: sess.Expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := sessionFrom(r); ok {
		s.d.Sessions.Delete(sess.Token)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
