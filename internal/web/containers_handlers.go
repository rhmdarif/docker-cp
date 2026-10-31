package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docker-cp/internal/docker"
	"docker-cp/internal/ports"
)

const (
	streamTimeout = 30 * time.Minute
	firstAutoPort = 8001
)

func (s *Server) containerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /containers", s.containersList)
	mux.HandleFunc("GET /containers/new", s.containerNew)
	mux.HandleFunc("POST /containers", s.containerCreate)
	mux.HandleFunc("POST /containers/{id}/recreate", s.containerRecreate)
	mux.HandleFunc("POST /containers/{id}/{action}", s.containerAction)
	mux.HandleFunc("GET /containers/{id}/logs", s.containerLogsPage)
	mux.HandleFunc("GET /containers/{id}/logs/stream", s.containerLogsStream)
	mux.HandleFunc("GET /containers/{id}/stats", s.containerStats)
}

// ---- streaming helpers ----

// progressWriter writes flushed text/plain progress. After the first write
// error (client gone) it drops output silently so a half-done operation keeps
// running instead of aborting.
type progressWriter struct {
	w    http.ResponseWriter
	dead bool
}

func newProgressWriter(w http.ResponseWriter) *progressWriter {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	return &progressWriter{w: w}
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if !p.dead {
		if _, err := p.w.Write(b); err != nil {
			p.dead = true
		} else if f, ok := p.w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return len(b), nil
}

func (p *progressWriter) printf(format string, a ...any) { fmt.Fprintf(p, format, a...) }

// detached returns a context that survives client disconnects, bounded by
// streamTimeout.
func detached(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), streamTimeout)
}

// ---- list ----

type containerRow struct {
	docker.ContainerSummary
	Err string
}

func (s *Server) containersList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	data := struct {
		Containers []containerRow
		Error      string
	}{}
	if err != nil {
		data.Error = err.Error()
	}
	for _, c := range cs {
		data.Containers = append(data.Containers, containerRow{ContainerSummary: c})
	}
	s.render(w, r, "containers", data)
}

// ---- form ----

type containerForm struct {
	Name, Image, Ports, Env, Mounts, Restart, Network, Cmd string
	PublishAll                                             bool
	Error                                                  string
	Warnings                                               []string
}

func (s *Server) containerNew(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	if err != nil {
		http.Error(w, "Docker: "+err.Error(), http.StatusBadGateway)
		return
	}
	used := ports.UsedPorts(cs, s.d.Store.Get().Settings.PanelPort)
	start := firstAutoPort
	next := func() int {
		p := ports.NextFree(start, used, s.listening)
		if p > 0 {
			used[p] = true
			start = p + 1
		}
		return p
	}

	form := containerForm{Restart: "unless-stopped"}
	if from := r.URL.Query().Get("from"); from != "" {
		d, err := s.d.Docker.Inspect(ctx, from)
		if err != nil {
			status := http.StatusBadGateway
			if docker.IsNotFound(err) {
				status = http.StatusNotFound
			}
			http.Error(w, "Container sumber: "+err.Error(), status)
			return
		}
		names := map[string]bool{}
		for _, c := range cs {
			names[c.Name] = true
		}
		spec := ports.Duplicate(d.Spec, ports.SuggestName(d.Spec.Name, func(n string) bool { return names[n] }), next)
		form.Name, form.Image, form.Restart, form.Network = spec.Name, spec.Image, spec.Restart, spec.Network
		form.Ports, form.Env, form.Mounts = FormatPorts(spec.Ports), FormatEnv(spec.Env), FormatMounts(spec.Mounts)
		form.Cmd = strings.Join(spec.Cmd, " ")
		for _, m := range spec.Mounts {
			if m.Type == "bind" {
				form.Warnings = append(form.Warnings, fmt.Sprintf("Peringatan: bind mount dipakai bersama sumber: %s (%s).", m.Source, d.Spec.Name))
			}
		}
	} else if p := next(); p > 0 {
		form.Ports = fmt.Sprintf("%d:80", p)
	}
	s.render(w, r, "container_form", form)
}

// parseContainerForm validates the POST body into a CreateSpec. The returned
// containerForm echoes the raw input for re-rendering.
func parseContainerForm(r *http.Request) (docker.CreateSpec, containerForm, error) {
	f := containerForm{
		Name: strings.TrimSpace(r.PostFormValue("name")), Image: strings.TrimSpace(r.PostFormValue("image")),
		Ports: r.PostFormValue("ports"), Env: r.PostFormValue("env"), Mounts: r.PostFormValue("mounts"),
		Restart: r.PostFormValue("restart"), Network: strings.TrimSpace(r.PostFormValue("network")),
		Cmd: r.PostFormValue("cmd"), PublishAll: r.PostFormValue("publish_all") == "on",
	}
	spec := docker.CreateSpec{Name: f.Name, Image: f.Image, Restart: f.Restart, Network: f.Network, Cmd: strings.Fields(f.Cmd)}
	if !containerNameRe.MatchString(f.Name) {
		return spec, f, fmt.Errorf("nama container harus cocok ^[a-zA-Z0-9][a-zA-Z0-9_.-]+$")
	}
	if f.Image == "" || strings.ContainsAny(f.Image, " \t\r\n") {
		return spec, f, fmt.Errorf("image wajib diisi")
	}
	switch f.Restart {
	case "", "no", "always", "unless-stopped", "on-failure":
	default:
		return spec, f, fmt.Errorf("kebijakan restart %q tidak dikenal", f.Restart)
	}
	var err error
	if spec.Ports, err = ParsePorts(f.Ports); err != nil {
		return spec, f, fmt.Errorf("port: %w", err)
	}
	if spec.Env, err = ParseEnv(f.Env); err != nil {
		return spec, f, fmt.Errorf("env: %w", err)
	}
	if spec.Mounts, err = ParseMounts(f.Mounts); err != nil {
		return spec, f, fmt.Errorf("mount: %w", err)
	}
	if f.PublishAll {
		for i := range spec.Ports {
			if spec.Ports[i].HostIP == "" {
				spec.Ports[i].HostIP = "0.0.0.0"
			}
		}
	}
	return spec, f, nil
}

func (s *Server) containerCreate(w http.ResponseWriter, r *http.Request) {
	spec, form, err := parseContainerForm(r)
	if err != nil {
		form.Error = err.Error()
		s.renderStatus(w, r, http.StatusBadRequest, "container_form", form)
		return
	}
	ctx, cancel := detached(r)
	defer cancel()
	out := newProgressWriter(w)

	if form.PublishAll {
		out.printf("PERINGATAN: port dipublikasikan di 0.0.0.0. Docker melewati ufw, jadi port ini terbuka ke internet.\n")
	}
	auth, err := docker.RegistryAuth(s.d.Cfg.DockerConfig, spec.Image)
	if err != nil {
		out.printf("Peringatan: kredensial registry diabaikan: %v\n", err)
		auth = ""
	}
	out.printf("Menarik image %s...\n", spec.Image)
	if err := s.d.Docker.Pull(ctx, spec.Image, auth, out); err != nil {
		out.printf("GAGAL menarik image: %v\n", err)
		return
	}
	out.printf("Membuat container %s...\n", spec.Name)
	id, err := s.d.Docker.Create(ctx, spec)
	if err != nil {
		out.printf("GAGAL membuat container: %v\n", err)
		return
	}
	out.printf("Menjalankan container...\n")
	if err := s.d.Docker.Start(ctx, id); err != nil {
		out.printf("GAGAL menjalankan container: %v (container sudah dibuat, cek halaman Containers)\n", err)
		s.reportCaddy(ctx, out)
		return
	}
	s.reportCaddy(ctx, out)
	out.printf("Selesai. Kembali ke /containers\n")
}

func (s *Server) reportCaddy(ctx context.Context, out *progressWriter) {
	for _, m := range s.applyCaddy(ctx) {
		out.printf("Caddy: %s\n", m)
	}
}

func (s *Server) containerRecreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := detached(r)
	defer cancel()
	out := newProgressWriter(w)
	d, err := s.d.Docker.Inspect(ctx, id)
	if err != nil {
		out.printf("GAGAL: %v\n", err)
		return
	}
	auth, err := docker.RegistryAuth(s.d.Cfg.DockerConfig, d.Spec.Image)
	if err != nil {
		out.printf("Peringatan: kredensial registry diabaikan: %v\n", err)
		auth = ""
	}
	out.printf("Membuat ulang %s dari %s...\n", d.Name, d.Spec.Image)
	if _, err := s.d.Docker.Recreate(ctx, id, auth, out); err != nil {
		out.printf("GAGAL: %v\n", err)
		return
	}
	s.reportCaddy(ctx, out)
	out.printf("Selesai.\n")
}

// ---- actions ----

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var err error
	switch action {
	case "start":
		err = s.d.Docker.Start(ctx, id)
	case "stop":
		err = s.d.Docker.Stop(ctx, id)
	case "restart":
		err = s.d.Docker.Restart(ctx, id)
	case "remove":
		err = s.d.Docker.Remove(ctx, id, r.PostFormValue("volumes") == "on")
	default:
		http.NotFound(w, r)
		return
	}
	var warnings []string
	if action == "remove" && err == nil {
		warnings = s.applyCaddy(ctx)
		if len(warnings) == 0 {
			// Empty body: htmx swaps the row away.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			return
		}
	}
	row := containerRow{}
	if err != nil {
		row.Err = err.Error()
	} else if len(warnings) > 0 {
		row.Err = "Container dihapus. " + strings.Join(warnings, " ")
	}
	cs, lerr := s.d.Docker.ListContainers(ctx)
	if lerr != nil && row.Err == "" {
		row.Err = lerr.Error()
	}
	for _, c := range cs {
		if c.ID == id || c.Name == id || (len(id) >= 4 && strings.HasPrefix(c.ID, id)) {
			row.ContainerSummary = c
		}
	}
	if row.ID == "" {
		if row.Err == "" {
			row.Err = "Container tidak ditemukan"
		}
		row.ID, row.Name = id, id
	}
	s.renderFragment(w, "containers", "container_row", row)
}

// ---- logs ----

func (s *Server) containerLogsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "container_logs", struct{ ID string }{r.PathValue("id")})
}

// sseLines turns written bytes into SSE "data:" events, one per line.
type sseLines struct {
	w   io.Writer
	f   http.Flusher
	buf []byte
	err error
}

const maxSSELine = 64 << 10

func (l *sseLines) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for l.err == nil {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) < maxSSELine {
				break
			}
			i = maxSSELine
			l.emit(l.buf[:i])
			l.buf = l.buf[i:]
			continue
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	return len(p), l.err
}

func (l *sseLines) emit(line []byte) {
	if l.err != nil {
		return
	}
	_, l.err = fmt.Fprintf(l.w, "data: %s\n\n", strings.ReplaceAll(string(line), "\r", ""))
	if l.err == nil && l.f != nil {
		l.f.Flush()
	}
}

func (l *sseLines) flushRest() {
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

func (s *Server) containerLogsStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && v > 0 {
		tail = min(v, 10000)
	}
	ctx := r.Context()
	d, err := s.d.Docker.Inspect(ctx, id)
	if err != nil {
		status := http.StatusBadGateway
		if docker.IsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	rc, err := s.d.Docker.Logs(ctx, id, tail, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	out := &sseLines{w: w, f: f}
	if d.Tty {
		_, err = io.Copy(out, rc)
	} else {
		err = docker.DemuxLogs(rc, out)
	}
	out.flushRest()
	if err != nil && ctx.Err() == nil {
		log.Printf("logs %s: %v", id, err)
	}
}

// ---- stats ----

func (s *Server) containerStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	st, err := s.d.Docker.StatsOnce(ctx, r.PathValue("id"))
	data := struct {
		Stats docker.Stats
		Error string
	}{Stats: st}
	if err != nil {
		data.Error = err.Error()
	}
	s.renderFragment(w, "stats", "stats", data)
}
