package web

import (
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"docker-cp/internal/docker"
)

func TestParsePorts(t *testing.T) {
	got, err := ParsePorts("8001:80\n0.0.0.0:9000:9000/udp\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []docker.PortBinding{{HostPort: 8001, ContainerPort: 80, Proto: "tcp"}, {HostIP: "0.0.0.0", HostPort: 9000, ContainerPort: 9000, Proto: "udp"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for _, bad := range []string{"abc", "70000:80", "8001:0", "8001:80/icmp", "x.y:1:2"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("ParsePorts(%q) = nil error", bad)
		}
	}
}

func TestParseEnv(t *testing.T) {
	got, err := ParseEnv("A=1\n# x\nB=a=b")
	if err != nil || !reflect.DeepEqual(got, []string{"A=1", "B=a=b"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"1A=x", "NOEQUALS", "=x"} {
		if _, err := ParseEnv(bad); err == nil {
			t.Errorf("ParseEnv(%q) = nil error", bad)
		}
	}
}

func TestParseMounts(t *testing.T) {
	got, err := ParseMounts("data:/data\n/srv/x:/x:ro")
	if err != nil {
		t.Fatal(err)
	}
	want := []docker.Mount{{Type: "volume", Source: "data", Target: "/data"}, {Type: "bind", Source: "/srv/x", Target: "/x", ReadOnly: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for _, bad := range []string{"data", "data:rel", "data:/x:rw2", ":/x"} {
		if _, err := ParseMounts(bad); err == nil {
			t.Errorf("ParseMounts(%q) = nil error", bad)
		}
	}
}

func TestFormatRoundTrip(t *testing.T) {
	ports := []docker.PortBinding{{HostPort: 8001, ContainerPort: 80, Proto: "tcp"}, {HostIP: "0.0.0.0", HostPort: 9000, ContainerPort: 9000, Proto: "udp"}}
	if s := FormatPorts(ports); s != "8001:80\n0.0.0.0:9000:9000/udp" {
		t.Fatalf("FormatPorts = %q", s)
	}
	if back, err := ParsePorts(FormatPorts(ports)); err != nil || !reflect.DeepEqual(back, ports) {
		t.Fatalf("ports round trip: %v %v", back, err)
	}
	mounts := []docker.Mount{{Type: "volume", Source: "data", Target: "/data"}, {Type: "bind", Source: "/srv/x", Target: "/x", ReadOnly: true}}
	if s := FormatMounts(mounts); s != "data:/data\n/srv/x:/x:ro" {
		t.Fatalf("FormatMounts = %q", s)
	}
	if back, err := ParseMounts(FormatMounts(mounts)); err != nil || !reflect.DeepEqual(back, mounts) {
		t.Fatalf("mounts round trip: %v %v", back, err)
	}
	if s := FormatEnv([]string{"A=1", "B=2"}); s != "A=1\nB=2" {
		t.Fatalf("FormatEnv = %q", s)
	}
}

func TestCmdRoundTrip(t *testing.T) {
	for _, cmd := range [][]string{
		{"nginx", "-g", "daemon off;"},
		{"sh", "-c", `echo "it's" \ok`},
		{"a", ""},
		{"plain"},
		nil,
	} {
		got, err := ParseCmd(FormatCmd(cmd))
		if err != nil || !reflect.DeepEqual(got, cmd) {
			t.Errorf("round trip %q via %q: got %q, %v", cmd, FormatCmd(cmd), got, err)
		}
	}
	if s := FormatCmd([]string{"nginx", "-g", "daemon off;"}); s != `nginx -g 'daemon off;'` {
		t.Errorf("FormatCmd = %q", s)
	}
}

func TestParseCmd(t *testing.T) {
	for in, want := range map[string][]string{
		`nginx -g "daemon off;"`: {"nginx", "-g", "daemon off;"},
		`a\ b 'c d' "e\"f" g\\h`: {"a b", "c d", `e"f`, `g\h`},
		"  x\t y\n":              {"x", "y"},
		`'a'"b"c`:                {"abc"},
		`"$HOME" '\n'`:           {"$HOME", `\n`},
	} {
		got, err := ParseCmd(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseCmd(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`"open`, `'open`, `trail\`} {
		if _, err := ParseCmd(bad); err == nil {
			t.Errorf("ParseCmd(%q) = nil error", bad)
		}
	}
}

func TestContainerFormNormalizesNewlines(t *testing.T) {
	r := httptest.NewRequest("POST", "/containers", strings.NewReader(url.Values{
		"name": {"app1"}, "image": {"nginx"}, "ports": {"8001:80\r8002:81"}, "env": {"A=1\rB=2"}, "mounts": {"data:/data\r\n"},
	}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	spec, _, err := parseContainerForm(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Ports) != 2 || !reflect.DeepEqual(spec.Env, []string{"A=1", "B=2"}) || len(spec.Mounts) != 1 {
		t.Fatalf("spec %+v", spec)
	}
}
