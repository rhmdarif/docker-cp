package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"docker-cp/internal/sysinfo"
)

type dashboardData struct {
	DockerError string
	Running     int
	Stopped     int
	Sites       int
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{Sites: len(s.d.Store.Get().Sites)}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cs, err := s.d.Docker.ListContainers(ctx)
	if err != nil {
		log.Printf("dashboard: list containers: %v", err)
		data.DockerError = err.Error()
	}
	for _, c := range cs {
		if c.State == "running" {
			data.Running++
		} else {
			data.Stopped++
		}
	}
	s.render(w, r, "dashboard", data)
}

type sysinfoData struct {
	Info  sysinfo.Info
	Error string
}

func (s *Server) sysinfoFragment(w http.ResponseWriter, r *http.Request) {
	info, err := sysinfo.Read(s.procRoot, s.diskPath, s.cpuSample)
	data := sysinfoData{Info: info}
	if err != nil {
		data.Error = err.Error()
	}
	s.renderFragment(w, "sysinfo", "sysinfo", data)
}
