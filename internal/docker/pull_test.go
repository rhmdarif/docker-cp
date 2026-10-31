package docker_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docker-cp/internal/docker"
	"docker-cp/internal/docker/dockertest"
)

func TestPullSendsAuth(t *testing.T) {
	f := dockertest.New(t)
	var hdr string
	var from, tag string
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Get("X-Registry-Auth")
		from, tag = r.URL.Query().Get("fromImage"), r.URL.Query().Get("tag")
		io.WriteString(w, `{"status":"Pulling from x","id":"1.2"}`+"\n"+`{"status":"Downloading","id":"l1","progress":"[=>]"}`+"\n"+`{"error":"denied"}`+"\n")
	})
	var out bytes.Buffer
	err := f.Client().Pull(context.Background(), "localhost:5000/app", "TOKEN", &out)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err=%v", err)
	}
	if hdr != "TOKEN" || from != "localhost:5000/app" || tag != "latest" {
		t.Errorf("hdr=%q from=%q tag=%q", hdr, from, tag)
	}
	if want := "Pulling from x 1.2\nDownloading l1 [=>]\n"; out.String() != want {
		t.Errorf("progress=%q", out.String())
	}
}

func TestPullSplitsTag(t *testing.T) {
	f := dockertest.New(t)
	var from, tag string
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) {
		from, tag = r.URL.Query().Get("fromImage"), r.URL.Query().Get("tag")
	})
	if err := f.Client().Pull(context.Background(), "ghcr.io:443/me/app:1.2", "", nil); err != nil {
		t.Fatal(err)
	}
	if from != "ghcr.io:443/me/app" || tag != "1.2" {
		t.Errorf("from=%q tag=%q", from, tag)
	}
}

func TestRegistryAuth(t *testing.T) {
	dir := t.TempDir()
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cfg := filepath.Join(dir, "config.json")
	os.WriteFile(cfg, []byte(`{"auths":{"ghcr.io":{"auth":"`+enc("me:tok:en")+`"},"https://index.docker.io/v1/":{"auth":"`+enc("hub:pw")+`"}}}`), 0o600)

	decode := func(s string) string {
		b, err := base64.URLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	got, err := docker.RegistryAuth(cfg, "ghcr.io/me/app:1")
	if err != nil {
		t.Fatal(err)
	}
	if d := decode(got); !strings.Contains(d, `"username":"me"`) || !strings.Contains(d, `"password":"tok:en"`) || !strings.Contains(d, `"serveraddress":"ghcr.io"`) {
		t.Errorf("ghcr: %s", d)
	}
	got, _ = docker.RegistryAuth(cfg, "nginx")
	if d := decode(got); !strings.Contains(d, `"username":"hub"`) {
		t.Errorf("hub: %s", d)
	}
	got, _ = docker.RegistryAuth(cfg, "myuser/app")
	if d := decode(got); !strings.Contains(d, `"username":"hub"`) {
		t.Errorf("hub user repo: %s", d)
	}
	if got, err := docker.RegistryAuth(cfg, "quay.io/x/y"); got != "" || err != nil {
		t.Errorf("missing entry: %q %v", got, err)
	}
	if got, err := docker.RegistryAuth(filepath.Join(dir, "nope"), "nginx"); got != "" || err != nil {
		t.Errorf("missing file: %q %v", got, err)
	}
}
