package docker_test

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"docker-cp/internal/docker/dockertest"
)

func containersJSON(f *dockertest.Fake) {
	f.Handle("GET /v1.43/containers/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Id":"c1","Names":["/a"],"ImageID":"sha256:used","Mounts":[{"Name":"vol-used"}]}]`)
	})
}

func TestListImagesInUse(t *testing.T) {
	f := dockertest.New(t)
	containersJSON(f)
	f.Handle("GET /v1.43/images/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Id":"sha256:used","RepoTags":["nginx:1"],"Size":10,"Created":1700000000},{"Id":"sha256:free","RepoTags":null,"Size":5,"Created":1}]`)
	})
	got, err := f.Client().ListImages(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if !got[0].InUse || got[1].InUse || got[0].Tags[0] != "nginx:1" || got[0].Created.Unix() != 1700000000 || got[0].Size != 10 {
		t.Fatalf("got %+v", got)
	}
}

func TestListVolumesInUse(t *testing.T) {
	f := dockertest.New(t)
	containersJSON(f)
	f.Handle("GET /v1.43/volumes", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Volumes":[{"Name":"vol-used","Driver":"local","Mountpoint":"/m1"},{"Name":"vol-free","Driver":"local","Mountpoint":"/m2"}]}`)
	})
	got, err := f.Client().ListVolumes(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if !got[0].InUse || got[1].InUse || got[0].Mountpoint != "/m1" {
		t.Fatalf("got %+v", got)
	}
}

func TestNetworksBuiltin(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/networks", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Id":"1","Name":"bridge","Driver":"bridge"},{"Id":"2","Name":"host","Driver":"host"},{"Id":"3","Name":"none","Driver":"null"},{"Id":"4","Name":"mine","Driver":"bridge"}]`)
	})
	got, err := f.Client().ListNetworks(context.Background())
	if err != nil || len(got) != 4 {
		t.Fatalf("%v %v", got, err)
	}
	for i, want := range []bool{true, true, true, false} {
		if got[i].Builtin != want {
			t.Errorf("%s builtin=%v", got[i].Name, got[i].Builtin)
		}
	}
}

func TestPruneImagesFilter(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("POST /v1.43/images/prune", func(w http.ResponseWriter, r *http.Request) {
		if fl := r.URL.Query().Get("filters"); !strings.Contains(fl, `"dangling":["false"]`) {
			t.Errorf("filters=%q", fl)
		}
		io.WriteString(w, `{"SpaceReclaimed":12345}`)
	})
	n, err := f.Client().PruneImages(context.Background())
	if err != nil || n != 12345 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestRemoveAndPrune(t *testing.T) {
	f := dockertest.New(t)
	var q string
	f.Handle("DELETE /v1.43/images/img1", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("force")
		io.WriteString(w, `[]`)
	})
	for _, p := range []string{"DELETE /v1.43/volumes/v1", "DELETE /v1.43/networks/n1", "POST /v1.43/volumes/prune", "POST /v1.43/networks/prune"} {
		f.Handle(p, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	}
	c, ctx := f.Client(), context.Background()
	for _, err := range []error{c.RemoveImage(ctx, "img1", true), c.RemoveVolume(ctx, "v1"), c.RemoveNetwork(ctx, "n1"), c.PruneVolumes(ctx), c.PruneNetworks(ctx)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if q != "true" {
		t.Errorf("force=%q", q)
	}
	want := []string{"DELETE /v1.43/images/img1", "DELETE /v1.43/volumes/v1", "DELETE /v1.43/networks/n1", "POST /v1.43/volumes/prune", "POST /v1.43/networks/prune"}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls=%v", got)
	}
}
