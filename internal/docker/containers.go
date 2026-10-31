package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// PortBinding is a published container port. Proto defaults to "tcp".
type PortBinding struct {
	HostIP        string
	HostPort      int
	ContainerPort int
	Proto         string
}

// Mount is a volume or bind mount. Type is "volume" or "bind".
type Mount struct {
	Type, Source, Target string
	ReadOnly             bool
}

type ContainerSummary struct {
	ID, Name, Image, State, Status, Project string
	Ports                                   []PortBinding
	Labels                                  map[string]string
}

type ContainerDetail struct {
	ID, Name string
	Tty      bool
	Spec     CreateSpec
	State    string
	// LivePorts are the currently published ports (NetworkSettings.Ports);
	// empty while the container is not running.
	LivePorts []PortBinding
}

type CreateSpec struct {
	Name, Image, Restart, Network string
	Ports                         []PortBinding
	Env                           []string
	Mounts                        []Mount
	Cmd                           []string
	Labels                        map[string]string
}

func esc(s string) string { return url.PathEscape(s) }

func proto(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// ListContainers lists all containers, running or not.
func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	var raw []struct {
		ID     string
		Names  []string
		Image  string
		State  string
		Status string
		Ports  []struct {
			IP          string
			PrivatePort int
			PublicPort  int
			Type        string
		}
		Labels map[string]string
	}
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &raw); err != nil {
		return nil, err
	}
	out := make([]ContainerSummary, 0, len(raw))
	for _, r := range raw {
		s := ContainerSummary{
			ID: r.ID, Image: r.Image, State: r.State, Status: r.Status,
			Labels:  r.Labels,
			Project: r.Labels["com.docker.compose.project"],
		}
		if len(r.Names) > 0 {
			s.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		seen := map[string]bool{}
		for _, p := range r.Ports {
			if p.PublicPort == 0 {
				continue
			}
			k := fmt.Sprintf("%d/%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			if seen[k] {
				continue // IPv4 and IPv6 listings of the same port
			}
			seen[k] = true
			s.Ports = append(s.Ports, PortBinding{HostIP: p.IP, HostPort: p.PublicPort, ContainerPort: p.PrivatePort, Proto: proto(p.Type)})
		}
		out = append(out, s)
	}
	return out, nil
}

// Inspect returns the container detail, with Spec rebuilt from the inspect JSON.
func (c *Client) Inspect(ctx context.Context, id string) (ContainerDetail, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+esc(id)+"/json", nil, nil, nil)
	if err != nil {
		return ContainerDetail{}, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return ContainerDetail{}, err
	}
	var raw struct {
		ID              string
		Name            string
		State           struct{ Status string }
		Config          struct{ Tty bool }
		NetworkSettings struct {
			Ports map[string][]wirePortBinding
		}
	}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		return ContainerDetail{}, err
	}
	spec, err := SpecFromInspect(buf.Bytes())
	if err != nil {
		return ContainerDetail{}, err
	}
	return ContainerDetail{
		ID: raw.ID, Name: strings.TrimPrefix(raw.Name, "/"), Tty: raw.Config.Tty, Spec: spec, State: raw.State.Status,
		LivePorts: portsFromWire(raw.NetworkSettings.Ports),
	}, nil
}

type wirePortBinding struct {
	HostIp   string
	HostPort string
}

type wireMount struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool `json:",omitempty"`
}

type wireCreate struct {
	Image        string
	Env          []string          `json:",omitempty"`
	Cmd          []string          `json:",omitempty"`
	Labels       map[string]string `json:",omitempty"`
	ExposedPorts map[string]struct{}
	HostConfig   struct {
		PortBindings  map[string][]wirePortBinding
		Mounts        []wireMount
		RestartPolicy struct{ Name string }
		NetworkMode   string `json:",omitempty"`
	}
}

// Create creates a container from s and returns its ID.
func (c *Client) Create(ctx context.Context, s CreateSpec) (string, error) {
	var w wireCreate
	w.Image, w.Env, w.Cmd, w.Labels = s.Image, s.Env, s.Cmd, s.Labels
	w.ExposedPorts = map[string]struct{}{}
	w.HostConfig.PortBindings = map[string][]wirePortBinding{}
	for _, p := range s.Ports {
		key := strconv.Itoa(p.ContainerPort) + "/" + proto(p.Proto)
		ip := p.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		w.ExposedPorts[key] = struct{}{}
		w.HostConfig.PortBindings[key] = append(w.HostConfig.PortBindings[key], wirePortBinding{HostIp: ip, HostPort: strconv.Itoa(p.HostPort)})
	}
	for _, m := range s.Mounts {
		w.HostConfig.Mounts = append(w.HostConfig.Mounts, wireMount{Type: m.Type, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	w.HostConfig.RestartPolicy.Name = s.Restart
	if w.HostConfig.RestartPolicy.Name == "" {
		w.HostConfig.RestartPolicy.Name = "unless-stopped"
	}
	w.HostConfig.NetworkMode = s.Network

	body, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	if s.Name != "" {
		q.Set("name", s.Name)
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", q, bytes.NewReader(body), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ Id string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Id, nil
}

// SpecFromInspect rebuilds a CreateSpec from raw container inspect JSON.
func SpecFromInspect(raw []byte) (CreateSpec, error) {
	var r struct {
		Name   string
		Config struct {
			Image  string
			Env    []string
			Cmd    []string
			Labels map[string]string
		}
		HostConfig struct {
			PortBindings  map[string][]wirePortBinding
			RestartPolicy struct{ Name string }
			NetworkMode   string
		}
		Mounts []struct {
			Type, Name, Source, Destination string
			RW                              bool
		}
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return CreateSpec{}, err
	}
	s := CreateSpec{
		Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image,
		Restart: r.HostConfig.RestartPolicy.Name,
		Env:     r.Config.Env, Cmd: r.Config.Cmd, Labels: r.Config.Labels,
	}
	if nm := r.HostConfig.NetworkMode; nm != "default" {
		s.Network = nm
	}
	s.Ports = portsFromWire(r.HostConfig.PortBindings)
	for _, m := range r.Mounts {
		src := m.Source
		if m.Type == "volume" && m.Name != "" {
			src = m.Name
		}
		s.Mounts = append(s.Mounts, Mount{Type: m.Type, Source: src, Target: m.Destination, ReadOnly: !m.RW})
	}
	return s, nil
}

// portsFromWire converts a Docker "80/tcp" -> bindings map, sorted.
func portsFromWire(m map[string][]wirePortBinding) []PortBinding {
	var out []PortBinding
	for key, binds := range m {
		cp, pr, _ := strings.Cut(key, "/")
		cport, err := strconv.Atoi(cp)
		if err != nil {
			continue
		}
		for _, b := range binds {
			hp, _ := strconv.Atoi(b.HostPort)
			out = append(out, PortBinding{HostIP: b.HostIp, HostPort: hp, ContainerPort: cport, Proto: proto(pr)})
		}
	}
	sortPorts(out)
	return out
}

func sortPorts(p []PortBinding) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && (p[j].ContainerPort < p[j-1].ContainerPort ||
			p[j].ContainerPort == p[j-1].ContainerPort && p[j].HostPort < p[j-1].HostPort); j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

func (c *Client) action(ctx context.Context, id, verb string) error {
	return c.call(ctx, http.MethodPost, "/containers/"+esc(id)+"/"+verb, nil) // 304 is < 400, so nil
}

func (c *Client) Start(ctx context.Context, id string) error   { return c.action(ctx, id, "start") }
func (c *Client) Stop(ctx context.Context, id string) error    { return c.action(ctx, id, "stop") }
func (c *Client) Restart(ctx context.Context, id string) error { return c.action(ctx, id, "restart") }

// Remove force-removes a container, optionally with its anonymous volumes.
func (c *Client) Remove(ctx context.Context, id string, removeVolumes bool) error {
	q := url.Values{"force": {"true"}}
	if removeVolumes {
		q.Set("v", "true")
	}
	return c.call(ctx, http.MethodDelete, "/containers/"+esc(id), q)
}

func (c *Client) Rename(ctx context.Context, id, newName string) error {
	return c.call(ctx, http.MethodPost, "/containers/"+esc(id)+"/rename", url.Values{"name": {newName}})
}
