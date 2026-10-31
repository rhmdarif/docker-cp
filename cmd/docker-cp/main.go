package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docker-cp/internal/auth"
	"docker-cp/internal/caddy"
	"docker-cp/internal/compose"
	"docker-cp/internal/config"
	"docker-cp/internal/docker"
	"docker-cp/internal/store"
	"docker-cp/internal/web"
)

// version is set via -ldflags "-X main.version=...".
var version = "dev"

const usage = "usage: docker-cp <version|serve|passwd|setup>"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
	case "passwd", "setup":
		cfg := config.FromEnv(config.Default())
		cli := runPasswd
		if args[0] == "setup" {
			cli = runSetup
		}
		if err := cli(cfg, args[1:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", args[0], err)
			return 1
		}
	case "serve":
		if err := runServe(config.FromEnv(config.Default())); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	return 0
}

// runServe runs the panel until SIGTERM/SIGINT, then shuts down gracefully.
func runServe(cfg config.Config) error {
	st, err := store.Open(cfg.StatePath())
	if err != nil {
		return err
	}
	port := st.Get().Settings.PanelPort
	if port == 0 {
		port = 8800
	}
	listen := fmt.Sprintf(":%d", port)
	if v := os.Getenv("DCP_LISTEN"); v != "" {
		listen = v
	}

	dc := docker.New(cfg.DockerSock)
	srv, err := web.New(web.Deps{
		Cfg:      cfg,
		Store:    st,
		Sessions: auth.NewSessions(24*time.Hour, time.Now),
		Limiter:  auth.NewLimiter(5, 15*time.Minute, time.Now),
		Docker:   dc,
		Compose:  compose.NewManager(cfg.StacksDir, "docker"),
		Caddy:    &caddy.Applier{Admin: caddy.NewAdmin(cfg.CaddyAdmin), Path: cfg.CaddyfilePath, Resolve: caddy.DockerResolver(dc)},
		Exit:     os.Exit,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	hs := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Printf("docker-cp %s listening on %s", version, listen)

	// Best effort, after the listener is up and bounded: Caddy or Docker may
	// not be ready yet; applyCaddy logs any failure and Caddy keeps its
	// previous config when Docker is unreachable.
	applyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	srv.ApplyCaddy(applyCtx)
	cancel()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
