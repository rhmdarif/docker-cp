package web

import (
	"crypto/subtle"
	"net/http"

	"docker-cp/internal/auth"
)

// maxLoginChecks caps concurrent bcrypt evaluations in login.
const maxLoginChecks = 4

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

// maxLoginBody bounds the POST /login body.
const maxLoginBody = 8 << 10

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Permintaan terlalu besar atau tidak valid.", http.StatusRequestEntityTooLarge)
		return
	}
	ip := clientIP(r)
	username, password := r.PostFormValue("username"), r.PostFormValue("password")
	data := loginData{Username: username}
	admin := s.d.Store.Get().Admin
	if admin.PasswordHash == "" {
		data.NoAdmin = true
		s.render(w, r, "login", data)
		return
	}
	// Take records the attempt before bcrypt runs, so parallel requests
	// cannot all pass the check; success Resets the counter.
	key := auth.LimiterKey(ip)
	if !s.d.Limiter.Take(key) {
		data.Error = "Terlalu banyak percobaan gagal. Coba lagi dalam 15 menit."
		s.renderStatus(w, r, http.StatusTooManyRequests, "login", data)
		return
	}
	select {
	case s.loginSem <- struct{}{}:
		defer func() { <-s.loginSem }()
	case <-r.Context().Done():
		return
	}
	// Always run bcrypt so a wrong username costs the same as a wrong password.
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(admin.Username)) == 1
	passOK := s.checkPassword(admin.PasswordHash, password)
	if !userOK || !passOK {
		data.Error = "Username atau password salah"
		s.render(w, r, "login", data)
		return
	}
	s.d.Limiter.Reset(key)
	if old, ok := s.lookupSession(r); ok {
		s.d.Sessions.Delete(old.Token)
	}
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
