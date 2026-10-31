// Package web is the HTTP panel: routing, middleware, templates and handlers.
//
// Files: server.go (Server, routes, static, Caddy lock), middleware.go
// (headers, auth, CSRF, client IP/origin helpers), render.go (templates,
// render/fragment/flash, formatters).
//
// Conventions for handlers added by later tasks:
//
//   - Routing uses Go 1.22+ ServeMux method+path patterns, registered in
//     routes(), e.g. mux.HandleFunc("POST /containers/{id}/start", s.x).
//   - Middleware order: securityHeaders -> requireAuth (all paths except
//     /login and /static/) -> requireCSRF (every non-GET/HEAD request except
//     POST /login; token in X-CSRF-Token header or "csrf" body form field,
//     never the query string).
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
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
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

	// checkPassword is auth.CheckPassword; tests wrap it to count calls.
	checkPassword func(hash, pw string) bool
	loginSem      chan struct{} // bounds concurrent bcrypt checks to maxLoginChecks

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
	return &Server{
		d: d, pages: pages, checkPassword: auth.CheckPassword,
		loginSem: make(chan struct{}, maxLoginChecks),
		procRoot: "/proc", diskPath: "/", cpuSample: 200 * time.Millisecond,
	}, nil
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
