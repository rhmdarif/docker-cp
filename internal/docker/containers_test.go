package docker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"docker-cp/internal/docker"
	"docker-cp/internal/docker/dockertest"
)

func TestListContainers(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("all=%q", r.URL.Query().Get("all"))
		}
		io.WriteString(w, `[{"Id":"abc","Names":["/klien-a"],"Image":"myapp:1.2","State":"running","Ports":[{"IP":"127.0.0.1","PrivatePort":80,"PublicPort":8001,"Type":"tcp"}],"Labels":{"com.docker.compose.project":"shop"}}]`)
	})
	got, err := f.Client().ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "klien-a" || got[0].Project != "shop" ||
		len(got[0].Ports) != 1 || got[0].Ports[0].HostPort != 8001 || got[0].Ports[0].Proto != "tcp" {
		t.Fatalf("got %+v", got)
	}
}

func TestCreateBody(t *testing.T) {
	f := dockertest.New(t)
	var q string
	var body struct {
		HostConfig struct {
			PortBindings  map[string][]map[string]string
			RestartPolicy struct{ Name string }
			Mounts        []map[string]any
		}
	}
	f.Handle("POST /v1.43/containers/create", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("name")
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(201)
		io.WriteString(w, `{"Id":"newid"}`)
	})
	id, err := f.Client().Create(context.Background(), docker.CreateSpec{
		Name: "klien-b", Image: "myapp:1.2",
		Ports:  []docker.PortBinding{{HostPort: 8002, ContainerPort: 80}},
		Mounts: []docker.Mount{{Type: "volume", Source: "data-klien-b", Target: "/data"}},
	})
	if err != nil || id != "newid" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if q != "klien-b" {
		t.Errorf("name=%q", q)
	}
	pb := body.HostConfig.PortBindings["80/tcp"]
	if len(pb) != 1 || pb[0]["HostIp"] != "127.0.0.1" || pb[0]["HostPort"] != "8002" {
		t.Errorf("portbindings=%v", body.HostConfig.PortBindings)
	}
	if body.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("restart=%q", body.HostConfig.RestartPolicy.Name)
	}
	if len(body.HostConfig.Mounts) != 1 || body.HostConfig.Mounts[0]["Source"] != "data-klien-b" {
		t.Errorf("mounts=%v", body.HostConfig.Mounts)
	}
}

func TestStartNotModified(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("POST /v1.43/containers/c1/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) })
	if err := f.Client().Start(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []string{"POST /v1.43/containers/c1/start"}) {
		t.Errorf("calls=%v", got)
	}
}

func TestAPIError(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/x/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"No such container: x"}`)
	})
	_, err := f.Client().Inspect(context.Background(), "x")
	if !docker.IsNotFound(err) || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoveRenameQuery(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("DELETE /v1.43/containers/c1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("force") != "true" || r.URL.Query().Get("v") != "true" {
			t.Errorf("query=%v", r.URL.RawQuery)
		}
		w.WriteHeader(204)
	})
	f.Handle("POST /v1.43/containers/c1/rename", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "new" {
			t.Errorf("query=%v", r.URL.RawQuery)
		}
		w.WriteHeader(204)
	})
	c := f.Client()
	if err := c.Remove(context.Background(), "c1", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename(context.Background(), "c1", "new"); err != nil {
		t.Fatal(err)
	}
}

func TestSpecFromInspectRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/inspect.json")
	if err != nil {
		t.Fatal(err)
	}
	want := docker.CreateSpec{
		Name: "klien-a", Image: "myapp:1.2", Restart: "always", Network: "appnet",
		Ports:  []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: 8001, ContainerPort: 80, Proto: "tcp"}},
		Env:    []string{"APP_ENV=prod", "PORT=80"},
		Mounts: []docker.Mount{{Type: "volume", Source: "data-klien-a", Target: "/data"}, {Type: "bind", Source: "/srv/conf", Target: "/etc/conf", ReadOnly: true}},
		Cmd:    []string{"serve", "--fast"},
		Labels: map[string]string{"owner": "klien-a"},
	}
	got, err := docker.SpecFromInspect(raw)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("err=%v\ngot  %+v\nwant %+v", err, got, want)
	}

	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/abc123/json", func(w http.ResponseWriter, r *http.Request) { w.Write(raw) })
	d, err := f.Client().Inspect(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "abc123" || d.Name != "klien-a" || !d.Tty || d.State != "running" || !reflect.DeepEqual(d.Spec, want) {
		t.Errorf("detail=%+v", d)
	}
}
