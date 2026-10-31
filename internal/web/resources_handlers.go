package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"docker-cp/internal/docker"
)

func (s *Server) resourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /images", s.imagesPage)
	mux.HandleFunc("POST /images/pull", s.imagePull)
	mux.HandleFunc("POST /images/prune", s.imagePrune)
	mux.HandleFunc("POST /images/{id}/remove", s.imageRemove)
	mux.HandleFunc("GET /volumes", s.volumesPage)
	mux.HandleFunc("POST /volumes/prune", s.volumePrune)
	mux.HandleFunc("POST /volumes/{name}/remove", s.volumeRemove)
	mux.HandleFunc("GET /networks", s.networksPage)
	mux.HandleFunc("POST /networks/prune", s.networkPrune)
	mux.HandleFunc("POST /networks/{id}/remove", s.networkRemove)
}

func reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 30*time.Second)
}

// flashRedirect flashes msg (or the error) and redirects back to page.
func (s *Server) flashRedirect(w http.ResponseWriter, r *http.Request, page, okMsg string, err error) {
	if err != nil {
		s.setFlash(w, "Gagal: "+err.Error())
	} else {
		s.setFlash(w, okMsg)
	}
	http.Redirect(w, r, page, http.StatusSeeOther)
}

// ---- images ----

type imageRow struct {
	ID, Tags, Size string
	Created        time.Time
	InUse          bool
}

func (s *Server) imagesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	imgs, err := s.d.Docker.ListImages(ctx)
	data := struct {
		Images []imageRow
		Error  string
	}{}
	if err != nil {
		data.Error = err.Error()
	}
	for _, im := range imgs {
		tags := "<none>"
		if len(im.Tags) > 0 {
			tags = strings.Join(im.Tags, ", ")
		}
		data.Images = append(data.Images, imageRow{ID: im.ID, Tags: tags, Size: humanBytes(uint64(max(im.Size, 0))), Created: im.Created, InUse: im.InUse})
	}
	s.render(w, r, "images", data)
}

func (s *Server) imagePull(w http.ResponseWriter, r *http.Request) {
	image := strings.TrimSpace(r.PostFormValue("image"))
	if image == "" || strings.ContainsAny(image, " \t\r\n") {
		http.Error(w, "image wajib diisi", http.StatusBadRequest)
		return
	}
	ctx, cancel := detached(r)
	defer cancel()
	out := newProgressWriter(w)
	auth, err := docker.RegistryAuth(s.d.Cfg.DockerConfig, image)
	if err != nil {
		out.printf("Peringatan: kredensial registry diabaikan: %v\n", err)
		auth = ""
	}
	out.printf("Menarik image %s...\n", image)
	if err := s.d.Docker.Pull(ctx, image, auth, out); err != nil {
		out.printf("GAGAL: %v\n", err)
		return
	}
	out.printf("Selesai.\n")
}

func (s *Server) imageRemove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	err := s.d.Docker.RemoveImage(ctx, r.PathValue("id"), false)
	s.flashRedirect(w, r, "/images", "Image dihapus.", err)
}

func (s *Server) imagePrune(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	freed, err := s.d.Docker.PruneImages(ctx)
	s.flashRedirect(w, r, "/images", fmt.Sprintf("Image tak terpakai dibersihkan (%s dibebaskan).", humanBytes(freed)), err)
}

// ---- volumes ----

func (s *Server) volumesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	vols, err := s.d.Docker.ListVolumes(ctx)
	data := struct {
		Volumes []docker.Volume
		Error   string
	}{Volumes: vols}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, r, "volumes", data)
}

func (s *Server) volumeRemove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	err := s.d.Docker.RemoveVolume(ctx, r.PathValue("name"))
	s.flashRedirect(w, r, "/volumes", "Volume dihapus.", err)
}

func (s *Server) volumePrune(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	s.flashRedirect(w, r, "/volumes", "Volume tak terpakai dibersihkan.", s.d.Docker.PruneVolumes(ctx))
}

// ---- networks ----

func (s *Server) networksPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	nets, err := s.d.Docker.ListNetworks(ctx)
	data := struct {
		Networks []docker.Network
		Error    string
	}{Networks: nets}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, r, "networks", data)
}

func (s *Server) networkRemove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	err := s.d.Docker.RemoveNetwork(ctx, r.PathValue("id"))
	s.flashRedirect(w, r, "/networks", "Network dihapus.", err)
}

func (s *Server) networkPrune(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	s.flashRedirect(w, r, "/networks", "Network tak terpakai dibersihkan.", s.d.Docker.PruneNetworks(ctx))
}
