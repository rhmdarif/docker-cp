package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"docker-cp/internal/caddy"
	"docker-cp/internal/store"
)

// maxSiteBody bounds POST bodies of the sites and settings forms.
const maxSiteBody = 256 << 10

// applyTimeout bounds one candidate -> Apply -> Update sequence. The context
// ignores client disconnects so the sequence is never cut off halfway.
const applyTimeout = 60 * time.Second

func (s *Server) siteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites", s.sitesList)
	mux.HandleFunc("GET /sites/new", s.siteNew)
	mux.HandleFunc("GET /sites/{id}", s.siteEdit)
	mux.HandleFunc("POST /sites", s.siteSave)
	mux.HandleFunc("POST /sites/{id}", s.siteSave)
	mux.HandleFunc("POST /sites/{id}/delete", s.siteDelete)
}

// formError is a user-facing failure of a candidate -> Apply -> Update
// sequence, with the HTTP status the form is re-rendered with.
type formError struct {
	status int
	msg    string
}

func (e *formError) Error() string { return e.msg }

func invalid(err error) error {
	return &formError{http.StatusUnprocessableEntity, err.Error()}
}

var errSiteNotFound = &formError{http.StatusNotFound, "Site tidak ditemukan."}

// applyFailure converts an Apply/Adapt error: Caddy's own message verbatim
// (422), an unreachable Caddy as 502, anything else (e.g. host
// verification) as 422.
func applyFailure(err error) error {
	var ce *caddy.CaddyError
	if errors.As(err, &ce) {
		return &formError{http.StatusUnprocessableEntity, ce.Message}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &formError{http.StatusBadGateway, "Caddy tidak terhubung: " + err.Error()}
	}
	return &formError{http.StatusUnprocessableEntity, "Gagal menerapkan konfigurasi Caddy: " + err.Error()}
}

// commit applies candidate and, only if Caddy accepted it, persists the part
// of the state chosen by save. Must be called inside withApplyLock. If
// persisting fails, the previous state is re-applied (best effort).
func (s *Server) commit(ctx context.Context, prev, candidate store.State, save func(*store.State)) ([]string, error) {
	warnings, err := s.d.Caddy.Apply(ctx, candidate)
	for _, w := range warnings {
		log.Printf("caddy: warning: %s", w)
	}
	if err != nil {
		return warnings, applyFailure(err)
	}
	if err := s.d.Store.Update(func(st *store.State) error { save(st); return nil }); err != nil {
		if _, rerr := s.d.Caddy.Apply(ctx, prev); rerr != nil {
			log.Printf("caddy: rollback after store failure: %v", rerr)
		}
		return warnings, &formError{http.StatusInternalServerError, "Gagal menyimpan state: " + err.Error()}
	}
	return warnings, nil
}

// ---- list ----

type siteRow struct {
	store.Site
	Label, TargetText string
}

func (s *Server) sitesList(w http.ResponseWriter, r *http.Request) {
	s.renderSites(w, r, http.StatusOK, "")
}

func (s *Server) renderSites(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	st := s.d.Store.Get()
	data := struct {
		PanelDomain string
		PanelPort   int
		Sites       []siteRow
		Error       string
	}{PanelDomain: st.Settings.PanelDomain, PanelPort: st.Settings.PanelPort, Error: errMsg}
	for _, site := range st.Sites {
		data.Sites = append(data.Sites, siteRow{Site: site, Label: strings.Join(site.Domains, ", "), TargetText: targetText(site)})
	}
	s.renderStatus(w, r, status, "sites", data)
}

func targetText(site store.Site) string {
	switch {
	case site.Manual:
		return ""
	case site.Target.Container != "" && site.Target.ContainerPort > 0:
		return fmt.Sprintf("%s:%d", site.Target.Container, site.Target.ContainerPort)
	case site.Target.Container != "":
		return site.Target.Container
	default:
		return site.Target.Host
	}
}

// ---- form ----

type siteFormData struct {
	ID, Domains, TargetType, Container, ContainerPort, Host, Custom, ManualBlock string
	Manual                                                                       bool
	New                                                                          bool
	Containers                                                                   []string
	DockerError                                                                  string
	Error                                                                        string
}

// withContainers fills the target select. If Docker is unreachable only the
// host target is offered.
func (s *Server) withContainers(ctx context.Context, f *siteFormData) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	if err != nil {
		f.DockerError = "Docker tidak terhubung: " + err.Error() + ". Hanya target host yang tersedia."
		f.TargetType = "host"
		return
	}
	for _, c := range cs {
		f.Containers = append(f.Containers, c.Name)
	}
	if f.Container != "" && !slices.Contains(f.Containers, f.Container) {
		f.Containers = append(f.Containers, f.Container)
	}
}

func (s *Server) renderSiteForm(w http.ResponseWriter, r *http.Request, status int, f siteFormData) {
	s.withContainers(r.Context(), &f)
	s.renderStatus(w, r, status, "site_form", f)
}

func (s *Server) siteNew(w http.ResponseWriter, r *http.Request) {
	s.renderSiteForm(w, r, http.StatusOK, siteFormData{New: true, TargetType: "container", ContainerPort: "80"})
}

func (s *Server) siteEdit(w http.ResponseWriter, r *http.Request) {
	sites := s.d.Store.Get().Sites
	i := siteIndex(sites, r.PathValue("id"))
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	site := sites[i]
	f := siteFormData{
		ID: site.ID, Domains: strings.Join(site.Domains, "\n"), Container: site.Target.Container,
		Host: site.Target.Host, Custom: site.Custom, Manual: site.Manual, ManualBlock: site.ManualBlock,
		TargetType: "container",
	}
	if site.Target.Container == "" {
		f.TargetType = "host"
	}
	if site.Target.ContainerPort > 0 {
		f.ContainerPort = strconv.Itoa(site.Target.ContainerPort)
	}
	s.renderSiteForm(w, r, http.StatusOK, f)
}

func siteIndex(sites []store.Site, id string) int {
	return slices.IndexFunc(sites, func(x store.Site) bool { return x.ID == id })
}

func parseSiteForm(r *http.Request) siteFormData {
	return siteFormData{
		ID:            r.PathValue("id"),
		New:           r.PathValue("id") == "",
		Domains:       caddy.NormalizeNewlines(r.PostFormValue("domains")),
		TargetType:    r.PostFormValue("target_type"),
		Container:     strings.TrimSpace(r.PostFormValue("container")),
		ContainerPort: strings.TrimSpace(r.PostFormValue("container_port")),
		Host:          strings.TrimSpace(r.PostFormValue("host")),
		Custom:        caddy.NormalizeNewlines(r.PostFormValue("custom")),
		Manual:        r.PostFormValue("manual") != "",
		ManualBlock:   caddy.NormalizeNewlines(r.PostFormValue("manual_block")),
	}
}

func (s *Server) siteSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSiteBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Permintaan terlalu besar atau tidak valid.", http.StatusRequestEntityTooLarge)
		return
	}
	f := parseSiteForm(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyTimeout)
	defer cancel()

	var warnings []string
	err := s.withApplyLock(func() error {
		st := s.d.Store.Get()
		id := f.ID
		if id == "" {
			id = store.NewID()
		} else if siteIndex(st.Sites, id) < 0 {
			return errSiteNotFound
		}
		site, err := s.buildSite(ctx, f, id, st)
		if err != nil {
			return err
		}
		candidate := st
		candidate.Sites = slices.Clone(st.Sites)
		if i := siteIndex(candidate.Sites, id); i >= 0 {
			candidate.Sites[i] = site
		} else {
			candidate.Sites = append(candidate.Sites, site)
		}
		warnings, err = s.commit(ctx, st, candidate, func(n *store.State) { n.Sites = candidate.Sites })
		return err
	})
	if err != nil {
		var fe *formError
		if !errors.As(err, &fe) {
			fe = &formError{http.StatusInternalServerError, err.Error()}
		}
		if fe == errSiteNotFound {
			http.NotFound(w, r)
			return
		}
		f.Error = fe.msg
		s.renderSiteForm(w, r, fe.status, f)
		return
	}
	msg := "Site disimpan."
	if len(warnings) > 0 {
		msg += " Peringatan: " + strings.Join(warnings, "; ")
	}
	s.setFlash(w, msg)
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

// buildSite validates the form against st and returns the site to store.
// Manual blocks are adapted by Caddy (no /load) to learn their hosts.
func (s *Server) buildSite(ctx context.Context, f siteFormData, id string, st store.State) (store.Site, error) {
	panel := st.Settings.PanelDomain
	if f.Manual {
		block := strings.TrimSpace(f.ManualBlock)
		if err := caddy.ValidateManualText(block); err != nil {
			return store.Site{}, invalid(fmt.Errorf("blok manual tidak valid: %w", err))
		}
		adapted, err := s.d.Caddy.Admin.Adapt(ctx, block)
		if err != nil {
			return store.Site{}, applyFailure(err)
		}
		var others []string
		for _, o := range st.Sites {
			if o.ID != id {
				others = append(others, o.Domains...)
			}
		}
		if panel != "" {
			others = append(others, panel)
		}
		if err := caddy.ValidateManualAdapted(adapted, others); err != nil {
			return store.Site{}, invalid(err)
		}
		hosts, err := adaptedHosts(adapted)
		if err != nil {
			return store.Site{}, invalid(err)
		}
		domains, err := caddy.ValidateDomains(hosts, id, st.Sites, panel)
		if err != nil {
			return store.Site{}, invalid(err)
		}
		return store.Site{ID: id, Domains: domains, Manual: true, ManualBlock: block}, nil
	}

	var raw []string
	for _, line := range strings.Split(f.Domains, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			raw = append(raw, line)
		}
	}
	domains, err := caddy.ValidateDomains(raw, id, st.Sites, panel)
	if err != nil {
		return store.Site{}, invalid(err)
	}
	custom := strings.TrimSpace(f.Custom)
	if err := caddy.ValidateCustom(custom); err != nil {
		return store.Site{}, invalid(fmt.Errorf("direktif custom tidak valid: %w", err))
	}
	target, err := parseTarget(f)
	if err != nil {
		return store.Site{}, invalid(err)
	}
	return store.Site{ID: id, Domains: domains, Target: target, Custom: custom}, nil
}

var hostNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func parseTarget(f siteFormData) (store.Target, error) {
	switch f.TargetType {
	case "container":
		if !containerNameRe.MatchString(f.Container) {
			return store.Target{}, errors.New("pilih container target")
		}
		port := 0
		if f.ContainerPort != "" {
			p, err := strconv.Atoi(f.ContainerPort)
			if err != nil || p < 1 || p > 65535 {
				return store.Target{}, errors.New("port container harus 1-65535")
			}
			port = p
		}
		return store.Target{Container: f.Container, ContainerPort: port}, nil
	case "host":
		if err := validUpstream(f.Host); err != nil {
			return store.Target{}, err
		}
		return store.Target{Host: f.Host}, nil
	default:
		return store.Target{}, errors.New("jenis target tidak valid")
	}
}

// validUpstream accepts host:port where host is an IP or a hostname. The
// value is written verbatim after reverse_proxy, so nothing else may pass.
func validUpstream(hp string) error {
	bad := fmt.Errorf("target host harus berbentuk host:port, mis. 127.0.0.1:9000: %q", hp)
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return bad
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return bad
	}
	if _, err := netip.ParseAddr(host); err != nil && !hostNameRe.MatchString(host) {
		return bad
	}
	return nil
}

// adaptedHosts returns the lowercased, de-duplicated host matchers of the
// top-level routes of an /adapt result, in order of appearance.
func adaptedHosts(adapted json.RawMessage) ([]string, error) {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
						} `json:"match"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(adapted, &cfg); err != nil {
		return nil, fmt.Errorf("hasil adapt tidak valid: %w", err)
	}
	names := make([]string, 0, len(cfg.Apps.HTTP.Servers))
	for n := range cfg.Apps.HTTP.Servers {
		names = append(names, n)
	}
	slices.Sort(names)
	var out []string
	for _, n := range names {
		for _, rt := range cfg.Apps.HTTP.Servers[n].Routes {
			for _, m := range rt.Match {
				for _, h := range m.Host {
					h = strings.ToLower(strings.TrimSpace(h))
					if !slices.Contains(out, h) {
						out = append(out, h)
					}
				}
			}
		}
	}
	return out, nil
}

// ---- delete ----

func (s *Server) siteDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyTimeout)
	defer cancel()
	err := s.withApplyLock(func() error {
		st := s.d.Store.Get()
		i := siteIndex(st.Sites, id)
		if i < 0 {
			return errSiteNotFound
		}
		candidate := st
		candidate.Sites = slices.Delete(slices.Clone(st.Sites), i, i+1)
		_, err := s.commit(ctx, st, candidate, func(n *store.State) { n.Sites = candidate.Sites })
		return err
	})
	if err != nil {
		var fe *formError
		if !errors.As(err, &fe) {
			fe = &formError{http.StatusInternalServerError, err.Error()}
		}
		if fe == errSiteNotFound {
			http.NotFound(w, r)
			return
		}
		s.renderSites(w, r, fe.status, "Gagal menghapus site: "+fe.msg)
		return
	}
	s.setFlash(w, "Site dihapus.")
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}
