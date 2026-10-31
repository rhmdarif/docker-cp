package ports

import (
	"testing"

	"docker-cp/internal/docker"
)

func TestNextFree(t *testing.T) {
	used := map[int]bool{8001: true, 8002: true}
	got := NextFree(8001, used, func(p int) bool { return p == 8003 })
	if got != 8004 {
		t.Fatalf("got %d want 8004", got)
	}
	if got := NextFree(65535, map[int]bool{65535: true}, func(int) bool { return false }); got != 0 {
		t.Fatalf("exhausted: got %d want 0", got)
	}
}

func TestUsedPortsIncludesExtra(t *testing.T) {
	cs := []docker.ContainerSummary{{Ports: []docker.PortBinding{{HostPort: 8001}}}}
	u := UsedPorts(cs, 8800)
	if !u[8800] || !u[8001] {
		t.Fatalf("missing ports: %v", u)
	}
	if !UsedPorts(nil, 8800)[8800] {
		t.Fatal("extra missing")
	}
}

func TestSuggestName(t *testing.T) {
	ex := map[string]bool{"klien-a": true, "klien-a-2": true}
	if got := SuggestName("klien-a", func(n string) bool { return ex[n] }); got != "klien-a-3" {
		t.Fatalf("got %q", got)
	}
	if got := SuggestName("app-2", func(n string) bool { return n == "app-2" }); got != "app-3" {
		t.Fatalf("got %q", got)
	}
}

func TestDuplicate(t *testing.T) {
	src := docker.CreateSpec{
		Name:  "klien-a",
		Ports: []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: 8001, ContainerPort: 80, Proto: "tcp"}},
		Env:   []string{"A=1"},
		Cmd:   []string{"run"},
		Mounts: []docker.Mount{
			{Type: "volume", Source: "data", Target: "/data"},
			{Type: "bind", Source: "/srv/x", Target: "/x"},
		},
		Labels: map[string]string{"com.docker.compose.project": "p", "keep": "y"},
	}
	n := 8001
	out := Duplicate(src, "klien-b", func() int { n++; return n })
	if out.Name != "klien-b" || out.Ports[0].HostPort != 8002 || out.Ports[0].ContainerPort != 80 || out.Ports[0].HostIP != "127.0.0.1" {
		t.Fatalf("bad ports/name: %+v", out)
	}
	if out.Mounts[0].Source != "data-klien-b" || out.Mounts[1].Source != "/srv/x" {
		t.Fatalf("bad mounts: %+v", out.Mounts)
	}
	if _, ok := out.Labels["com.docker.compose.project"]; ok || out.Labels["keep"] != "y" {
		t.Fatalf("bad labels: %v", out.Labels)
	}
	out.Ports[0].HostPort = 1
	out.Mounts[0].Source = "z"
	out.Env[0] = "B=2"
	out.Cmd[0] = "x"
	out.Labels["keep"] = "n"
	if src.Ports[0].HostPort != 8001 || src.Mounts[0].Source != "data" || src.Env[0] != "A=1" || src.Cmd[0] != "run" ||
		src.Labels["keep"] != "y" || src.Labels["com.docker.compose.project"] != "p" {
		t.Fatalf("source mutated: %+v", src)
	}
}
