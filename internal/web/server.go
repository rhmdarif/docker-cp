// Package web is the HTTP panel: routing, middleware, templates and handlers.
//
// Conventions for handlers added by later tasks:
//
//   - Routing uses Go 1.22+ ServeMux method+path patterns, registered in
//     routes(), e.g. mux.HandleFunc("POST /containers/{id}/start", s.x).
//   - Middleware order: securityHeaders -> requireAuth (all paths except
//     /login and /static/) -> requireCSRF (every non-GET/HEAD request except
//     POST /login; token in X-CSRF-Token header or "csrf" form field).
//     Handlers get the session via sessionFrom(r).
//   - Templates: templates/layout.html is the shell; every other
//     templates/<name>.html defines {{define "content"}} (optionally
//     {{define "title"}}) and is parsed into its own clone of the layout.
//     render(w, r, "<name>", data) executes the layout; the page sees
//     .Page, .CSRF, .Flash, .InsecureWarning, .Authed and its own data as
//     .Data. The active nav key is the template name up to the first "_"
//     ("containers_detail" highlights "containers"). Fragments (htmx
//     responses, no layout) are {{define "<block>"}} blocks in any page file,
//     executed with renderFragment(w, "<file>", "<block>", data) and given
//     data as-is (unwrapped).
//   - Flash messages survive one redirect: setFlash(w, msg) then 303; the
//     next render shows and clears it.
//   - Caddy: Applier is not goroutine-safe. applyCaddy takes applyMu. Code
//     that must run candidate -> Apply -> Store.Update atomically wraps the
//     whole sequence in withApplyLock and calls s.d.Caddy.Apply directly
//     inside it (never applyCaddy: the mutex is not reentrant).
//   - No background goroutines or tickers; periodic UI refresh is htmx
//     polling (hx-trigger "every Ns") that only runs while a page is open.
//   - No inline scripts or event handlers: behaviour lives in static/app.js
//     ([data-confirm] dialogs, form[data-stream] / streamPost streaming).
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"docker-cp/internal/auth"
	"docker-cp/internal/caddy"
	"docker-cp/internal/compose"
	"docker-cp/internal/config"
	"docker-cp/internal/docker"
	"docker-cp/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie = "dcp_session"
	flashCookie   = "dcp_flash"

	csp = "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; img-src 'self' data:"
)

// Deps are the collaborators the panel is built from.
type Deps struct {
	Cfg      config.Config
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Docker   *docker.Client
	Compose  *compose.Manager
	Caddy    *caddy.Applier
	Exit     func(code int) // used by Settings to restart itself; default os.Exit
}

// Server is the panel's HTTP application.
type Server struct {
	d       Deps
	applyMu sync.Mutex
	pages   map[string]*template.Template

	// sysinfo sources; fixed in production, overridable in tests.
	procRoot, diskPath string
	cpuSample          time.Duration
}

// New parses the embedded templates and returns a Server.
func New(d Deps) (*Server, error) {
	if d.Exit == nil {
		d.Exit = os.Exit
	}
	pages, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{d: d, pages: pages, procRoot: "/proc", diskPath: "/", cpuSample: 200 * time.Millisecond}, nil
}

var funcs = template.FuncMap{
	"bytes": humanBytes,
	"pct": func(used, total uint64) string {
		if total == 0 {
			return "0%"
		}
		return fmt.Sprintf("%.0f%%", float64(used)*100/float64(total))
	},
	"uptime":   humanDuration,
	"navItems": func() []navItem { return nav },
}

type navItem struct{ Key, Href, Label string }

// nav is the sidebar; Key matches the page template name (see navKey).
var nav = []navItem{
	{"dashboard", "/", "Dashboard"},
	{"containers", "/containers", "Containers"},
	{"stacks", "/stacks", "Stacks"},
	{"images", "/images", "Images"},
	{"volumes", "/volumes", "Volumes"},
	{"networks", "/networks", "Networks"},
	{"sites", "/sites", "Sites"},
	{"terminal", "/terminal", "Terminal"},
	{"settings", "/settings", "Pengaturan"},
}

func parseTemplates() (map[string]*template.Template, error) {
	base, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		if name == "layout" {
			continue
		}
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(templateFS, f); err != nil {
			return nil, fmt.Errorf("template %s: %w", f, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// Handler returns the full middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	return securityHeaders(s.requireAuth(s.requireCSRF(s.routes())))
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /fragments/sysinfo", s.sysinfoFragment)
	return mux
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=86400")
		files.ServeHTTP(w, r)
	})
}

// ---- middleware ----

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
			token = r.FormValue("csrf")
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

// ---- rendering ----

// pageData is what layout.html and every page template receive.
type pageData struct {
	Page            string // active nav key
	CSRF            string
	Flash           string
	InsecureWarning bool
	Authed          bool
	Data            any
}

func navKey(name string) string {
	if i := strings.IndexByte(name, '_'); i >= 0 {
		return name[:i]
	}
	return name
}

// render executes layout + page name with status 200.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.renderStatus(w, r, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	t, ok := s.pages[name]
	if !ok {
		log.Printf("web: unknown template %q", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pd := pageData{Page: navKey(name), InsecureWarning: !isHTTPS(r), Data: data}
	if sess, ok := sessionFrom(r); ok {
		pd.CSRF, pd.Authed = sess.CSRF, true
	}
	if c, err := r.Cookie(flashCookie); err == nil {
		if msg, err := url.QueryUnescape(c.Value); err == nil {
			pd.Flash = msg
		}
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", pd); err != nil {
		log.Printf("web: render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// renderFragment executes block from templates/<file>.html without the
// layout; data is passed as-is.
func (s *Server) renderFragment(w http.ResponseWriter, file, block string, data any) {
	t, ok := s.pages[file]
	if !ok {
		log.Printf("web: unknown template %q", file)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		log.Printf("web: render %s/%s: %v", file, block, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// setFlash stores msg to be shown by the next rendered page.
func (s *Server) setFlash(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(msg), Path: "/",
		MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// ---- caddy ----

// withApplyLock runs fn while holding applyMu.
func (s *Server) withApplyLock(fn func() error) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return fn()
}

// applyCaddy pushes the current state to Caddy. Warnings and the error (if
// any) are logged and returned as user-facing messages.
func (s *Server) applyCaddy(ctx context.Context) []string {
	var msgs []string
	s.withApplyLock(func() error {
		warnings, err := s.d.Caddy.Apply(ctx, s.d.Store.Get())
		for _, w := range warnings {
			log.Printf("caddy: warning: %s", w)
			msgs = append(msgs, w)
		}
		if err != nil {
			log.Printf("caddy: apply: %v", err)
			msgs = append(msgs, "Gagal menerapkan konfigurasi Caddy: "+err.Error())
		}
		return nil
	})
	return msgs
}

// ApplyCaddy is applyCaddy for callers outside the package (serve's
// best-effort apply at startup).
func (s *Server) ApplyCaddy(ctx context.Context) []string { return s.applyCaddy(ctx) }

// ---- formatting ----

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d hari %d jam", days, h)
	case h > 0:
		return fmt.Sprintf("%d jam %d menit", h, m)
	default:
		return fmt.Sprintf("%d menit", m)
	}
}
