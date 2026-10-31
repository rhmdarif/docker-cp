package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

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
