package web

import (
	"log"
	"net/http"

	"github.com/coder/websocket"

	"docker-cp/internal/terminal"
)

func (s *Server) terminalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /terminal", s.terminalPage)
	mux.HandleFunc("GET /ws/host", s.wsHost)
	mux.HandleFunc("GET /containers/{id}/terminal", s.containerTerminalPage)
	mux.HandleFunc("GET /ws/exec/{id}", s.wsExec)
}

func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "terminal", nil)
}

func (s *Server) containerTerminalPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "container_terminal", struct{ ID string }{r.PathValue("id")})
}

// acceptWS admits a WebSocket only for a valid session and a same-host
// Origin; anything else is answered 403 before the handshake. The library's
// own origin check stays enabled as a second layer.
func (s *Server) acceptWS(w http.ResponseWriter, r *http.Request) *websocket.Conn {
	if _, ok := s.lookupSession(r); !ok || !checkOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		log.Printf("terminal: accept: %v", err)
		return nil
	}
	return ws
}

func (s *Server) wsHost(w http.ResponseWriter, r *http.Request) {
	ws := s.acceptWS(w, r)
	if ws == nil {
		return
	}
	if err := terminal.ServeHost(r.Context(), ws, terminal.DefaultShell(), nil); err != nil {
		log.Printf("terminal: host: %v", err)
	}
}

func (s *Server) wsExec(w http.ResponseWriter, r *http.Request) {
	ws := s.acceptWS(w, r)
	if ws == nil {
		return
	}
	if err := terminal.ServeExec(r.Context(), ws, s.d.Docker, r.PathValue("id")); err != nil {
		log.Printf("terminal: exec %s: %v", r.PathValue("id"), err)
	}
}
