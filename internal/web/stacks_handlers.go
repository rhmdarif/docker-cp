package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"docker-cp/internal/compose"
)

func (s *Server) stackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /stacks", s.stacksList)
	mux.HandleFunc("GET /stacks/new", s.stackNew)
	mux.HandleFunc("GET /stacks/{name}", s.stackEdit)
	mux.HandleFunc("POST /stacks", s.stackSave)
	mux.HandleFunc("POST /stacks/{name}", s.stackSave)
	mux.HandleFunc("POST /stacks/{name}/deploy", s.stackDeploy)
	mux.HandleFunc("POST /stacks/{name}/{action}", s.stackAction)
	mux.HandleFunc("POST /projects/{name}/{action}", s.projectAction)
}

type stackForm struct {
	Name, Compose, Env string
	New                bool
	Error              string
}

func (s *Server) stacksList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	data := struct {
		Stacks []compose.Stack
		Error  string
	}{}
	if err != nil {
		data.Error = "Docker tidak terhubung: " + err.Error()
	}
	data.Stacks, err = s.d.Compose.List(cs)
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, r, "stacks", data)
}

func (s *Server) stackNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "stack_form", stackForm{New: true})
}

func (s *Server) stackEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !compose.ValidName(name) {
		http.Error(w, "nama stack tidak valid", http.StatusBadRequest)
		return
	}
	c, env, err := s.d.Compose.Read(name)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "stack_form", stackForm{Name: name, Compose: c, Env: env})
}

// stackName returns the stack name from the path, or from the "name" form
// field for POST /stacks (new stack).
func stackName(r *http.Request) string {
	if n := r.PathValue("name"); n != "" {
		return n
	}
	return r.PostFormValue("name")
}

// saveStack validates the name and writes the form; on failure it has
// already responded.
func (s *Server) saveStack(w http.ResponseWriter, r *http.Request) (string, bool) {
	form := stackForm{Name: stackName(r), Compose: r.PostFormValue("compose"), Env: r.PostFormValue("env"), New: r.PathValue("name") == ""}
	if !compose.ValidName(form.Name) {
		form.Error = "Nama stack tidak valid: huruf kecil, angka, - dan _, maksimal 63 karakter."
		s.renderStatus(w, r, http.StatusBadRequest, "stack_form", form)
		return "", false
	}
	if err := s.d.Compose.Write(form.Name, form.Compose, form.Env); err != nil {
		form.Error = "Gagal menyimpan: " + err.Error()
		s.renderStatus(w, r, http.StatusInternalServerError, "stack_form", form)
		return "", false
	}
	return form.Name, true
}

func (s *Server) stackSave(w http.ResponseWriter, r *http.Request) {
	name, ok := s.saveStack(w, r)
	if !ok {
		return
	}
	s.setFlash(w, "Stack "+name+" disimpan.")
	http.Redirect(w, r, "/stacks/"+url.PathEscape(name), http.StatusSeeOther)
}

// stackDeploy saves the posted files and then runs `up`, streaming output.
func (s *Server) stackDeploy(w http.ResponseWriter, r *http.Request) {
	name, ok := s.saveStack(w, r)
	if !ok {
		return
	}
	s.runStack(w, r, name, compose.ActionUp, true)
}

func (s *Server) stackAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	switch action {
	case "up", "stop", "down", "pull", "delete":
	default:
		http.NotFound(w, r)
		return
	}
	if !compose.ValidName(name) {
		http.Error(w, "nama stack tidak valid", http.StatusBadRequest)
		return
	}
	s.runStack(w, r, name, compose.Action(action), false)
}

func (s *Server) runStack(w http.ResponseWriter, r *http.Request, name string, a compose.Action, saved bool) {
	ctx, cancel := detached(r)
	defer cancel()
	out := newProgressWriter(w)
	if saved {
		out.printf("Tersimpan.\n")
	}
	var err error
	if a == "delete" {
		err = s.d.Compose.Delete(ctx, name, out)
	} else {
		err = s.d.Compose.Run(ctx, name, a, out)
	}
	if err != nil {
		out.printf("GAGAL: %v\n", err)
	}
	s.reportCaddy(ctx, out)
	if err == nil {
		out.printf("Selesai.\n")
	}
}

func (s *Server) projectAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	if action != "start" && action != "stop" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := detached(r)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	if err == nil {
		if action == "start" {
			err = compose.StartProject(ctx, s.d.Docker, cs, name)
		} else {
			err = compose.StopProject(ctx, s.d.Docker, cs, name)
		}
	}
	msg := "Project " + name + ": " + action + " selesai."
	if err != nil {
		msg = "Gagal: " + err.Error()
	}
	for _, m := range s.applyCaddy(ctx) {
		msg += " Caddy: " + m
	}
	s.setFlash(w, msg)
	http.Redirect(w, r, "/stacks", http.StatusSeeOther)
}
