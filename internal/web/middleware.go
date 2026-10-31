package web

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"docker-cp/internal/auth"
)

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

// sessionFrom returns the session attached by requireAuth.
func sessionFrom(r *http.Request) (auth.Session, bool) {
	sess, ok := r.Context().Value(ctxKey{}).(auth.Session)
	return sess, ok
}

func (s *Server) lookupSession(r *http.Request) (auth.Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return auth.Session{}, false
	}
	return s.d.Sessions.Lookup(c.Value)
}

func publicPath(p string) bool {
	return p == "/login" || strings.HasPrefix(p, "/static/")
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := s.lookupSession(r)
		if !ok {
			if r.Header.Get("HX-Request") != "" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || (r.Method == http.MethodPost && r.URL.Path == "/login") {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := sessionFrom(r)
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("csrf") // body only, never the query string
		}
		if !ok || !auth.ValidCSRF(sess, token) {
			http.Error(w, "Token CSRF tidak valid. Muat ulang halaman lalu coba lagi.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- request helpers ----

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func fromLoopback(r *http.Request) bool {
	ip := net.ParseIP(remoteHost(r))
	return ip != nil && ip.IsLoopback()
}

// clientIP is the peer address, or the last X-Forwarded-For entry when the
// peer is a loopback reverse proxy (Caddy appends the real client last).
func clientIP(r *http.Request) string {
	host := remoteHost(r)
	if !fromLoopback(r) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return host
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(last) == nil {
		return host
	}
	return last
}

// isHTTPS reports whether the browser reached the panel over TLS, directly or
// through a loopback reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (fromLoopback(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// checkOrigin reports whether the Origin header names the request's Host.
// A missing or opaque ("null") Origin is rejected; browsers always send one
// on WebSocket handshakes.
func checkOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
