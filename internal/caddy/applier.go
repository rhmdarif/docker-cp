package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"docker-cp/internal/docker"
	"docker-cp/internal/store"
)

// Applier renders the Caddyfile, checks it, pushes it to Caddy and persists it.
type Applier struct {
	Admin   *Admin
	Path    string
	Resolve Resolver
}

// Apply runs Generate -> Adapt -> verify -> Load -> atomic write of Path.
// Path is left untouched unless Caddy accepted the config.
func (a *Applier) Apply(ctx context.Context, st store.State) ([]string, error) {
	cf, warnings := Generate(st, a.Resolve)
	adapted, err := a.Admin.Adapt(ctx, cf)
	if err != nil {
		return warnings, err
	}
	if err := verifyHosts(adapted, expectedHosts(st)); err != nil {
		return warnings, err
	}
	if err := a.Admin.Load(ctx, cf); err != nil {
		return warnings, err
	}
	if err := writeAtomic(a.Path, []byte(cf)); err != nil {
		return warnings, &WriteError{Err: err}
	}
	return warnings, nil
}

// WriteError reports that Caddy accepted (loaded) the config but writing the
// Caddyfile failed, so the running config is not persisted.
type WriteError struct{ Err error }

func (e *WriteError) Error() string { return "gagal menulis Caddyfile: " + e.Err.Error() }
func (e *WriteError) Unwrap() error { return e.Err }

func expectedHosts(st store.State) []string {
	var out []string
	if st.Settings.PanelDomain != "" {
		out = append(out, st.Settings.PanelDomain)
	}
	for _, s := range st.Sites {
		out = append(out, s.Domains...)
	}
	return out
}

// verifyHosts checks that every top-level HTTP route is host-matched and that
// every host is one the panel knows about, so injected config cannot add
// catch-all routes or foreign hosts.
func verifyHosts(adapted json.RawMessage, expected []string) error {
	allowed := make(map[string]bool, len(expected))
	for _, h := range expected {
		allowed[strings.ToLower(h)] = true
	}
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
		return fmt.Errorf("konfigurasi Caddy tidak dapat dibaca: %w", err)
	}
	for name, srv := range cfg.Apps.HTTP.Servers {
		for i, r := range srv.Routes {
			if len(r.Match) == 0 {
				return fmt.Errorf("konfigurasi Caddy berisi rute tanpa pencocokan host (server %s, rute %d)", name, i)
			}
			for _, m := range r.Match {
				if len(m.Host) == 0 {
					return fmt.Errorf("konfigurasi Caddy berisi rute tanpa pencocokan host (server %s, rute %d)", name, i)
				}
				for _, h := range m.Host {
					if !allowed[strings.ToLower(h)] {
						return fmt.Errorf("konfigurasi Caddy berisi alamat tak terduga: %s", h)
					}
				}
			}
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".Caddyfile-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DockerResolver resolves a container target to its published host port on
// 127.0.0.1. A target with only Host set is returned unchanged.
func DockerResolver(dc *docker.Client) Resolver {
	return func(t store.Target) (string, error) {
		if t.Container == "" && t.Host != "" {
			return t.Host, nil
		}
		cs, err := dc.ListContainers(context.Background())
		if err != nil {
			return "", err
		}
		for _, c := range cs {
			if c.Name != t.Container {
				continue
			}
			for _, p := range c.Ports {
				if t.ContainerPort == 0 || p.ContainerPort == t.ContainerPort {
					return fmt.Sprintf("127.0.0.1:%d", p.HostPort), nil
				}
			}
			return "", fmt.Errorf("container tidak mem-publish port")
		}
		return "", fmt.Errorf("container %q tidak ditemukan", t.Container)
	}
}
