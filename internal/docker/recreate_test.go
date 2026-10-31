package docker_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"docker-cp/internal/docker/dockertest"
)

const inspectOld = `{"Id":"old1","Name":"/app","State":{"Status":"running"},"Config":{"Image":"myapp:1","Tty":false},"HostConfig":{"RestartPolicy":{"Name":"always"},"NetworkMode":"default"}}`

func recreateFake(t *testing.T, createStatus, newStartStatus int) (*dockertest.Fake, *[]string) {
	f := dockertest.New(t)
	var mu sync.Mutex
	var renames []string
	f.Handle("GET /v1.43/containers/old1/json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, inspectOld) })
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"Done"}`) })
	f.Handle("POST /v1.43/containers/old1/stop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("POST /v1.43/containers/old1/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("POST /v1.43/containers/old1/rename", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		renames = append(renames, r.URL.Query().Get("name"))
		mu.Unlock()
		w.WriteHeader(204)
	})
	f.Handle("POST /v1.43/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if createStatus != 201 {
			w.WriteHeader(createStatus)
			io.WriteString(w, `{"message":"boom"}`)
			return
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"Id":"new1"}`)
	})
	f.Handle("POST /v1.43/containers/new1/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(newStartStatus)
		if newStartStatus >= 400 {
			io.WriteString(w, `{"message":"nope"}`)
		}
	})
	f.Handle("DELETE /v1.43/containers/old1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			t.Errorf("remove old must not pass v: %v", r.URL.Query())
		}
		w.WriteHeader(204)
	})
	return f, &renames
}

func inOrder(t *testing.T, calls, want []string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("calls %v do not contain in order %v", calls, want)
	}
}

func TestRecreateHappyPath(t *testing.T) {
	f, renames := recreateFake(t, 201, 204)
	id, err := f.Client().Recreate(context.Background(), "old1", "", nil)
	if err != nil || id != "new1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	inOrder(t, f.Calls(), []string{
		"GET /v1.43/containers/old1/json", "POST /v1.43/images/create", "POST /v1.43/containers/old1/stop",
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/create",
		"POST /v1.43/containers/new1/start", "DELETE /v1.43/containers/old1",
	})
	if len(*renames) != 1 || !strings.HasPrefix((*renames)[0], "app-old-") {
		t.Errorf("renames=%v", *renames)
	}
}

func TestRecreateRollsBackOnCreateFailure(t *testing.T) {
	f, renames := recreateFake(t, 500, 204)
	_, err := f.Client().Recreate(context.Background(), "old1", "", nil)
	if err == nil {
		t.Fatal("want error")
	}
	inOrder(t, f.Calls(), []string{
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/create",
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/old1/start",
	})
	if len(*renames) != 2 || (*renames)[1] != "app" {
		t.Errorf("renames=%v", *renames)
	}
}

func TestRecreateRollsBackOnStartFailure(t *testing.T) {
	f, renames := recreateFake(t, 201, 500)
	f.Handle("DELETE /v1.43/containers/new1", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	if _, err := f.Client().Recreate(context.Background(), "old1", "", nil); err == nil {
		t.Fatal("want error")
	}
	inOrder(t, f.Calls(), []string{
		"GET /v1.43/containers/old1/json", "POST /v1.43/images/create", "POST /v1.43/containers/old1/stop",
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/create",
		"POST /v1.43/containers/new1/start", "DELETE /v1.43/containers/new1",
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/old1/start",
	})
	if len(*renames) != 2 || (*renames)[1] != "app" {
		t.Errorf("renames=%v", *renames)
	}
}

func TestRecreateReportsRollbackFailure(t *testing.T) {
	// old container refuses to start again during rollback
	f2, _ := recreateFake(t, 500, 204)
	f2.Handle("GET /v1.43/containers/old2/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Replace(inspectOld, "old1", "old2", 1))
	})
	f2.Handle("POST /v1.43/containers/old2/stop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f2.Handle("POST /v1.43/containers/old2/rename", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f2.Handle("POST /v1.43/containers/old2/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"message":"cannot start"}`)
	})
	_, err := f2.Client().Recreate(context.Background(), "old2", "", nil)
	if err == nil || !strings.Contains(err.Error(), "rollback start") || !strings.Contains(err.Error(), "create") {
		t.Fatalf("err=%v", err)
	}
}

func TestRecreatePreservesRawConfig(t *testing.T) {
	raw, err := os.ReadFile("testdata/inspect_full.json")
	if err != nil {
		t.Fatal(err)
	}
	f := dockertest.New(t)
	var mu sync.Mutex
	var createBody []byte
	var connect []string
	f.Handle("GET /v1.43/containers/old1/json", func(w http.ResponseWriter, r *http.Request) { w.Write(raw) })
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fromImage"); got != "myapp" {
			t.Errorf("pulled %q", got)
		}
		io.WriteString(w, `{"status":"Done"}`)
	})
	for _, p := range []string{"POST /v1.43/containers/old1/stop", "POST /v1.43/containers/old1/rename", "POST /v1.43/containers/new1/start", "DELETE /v1.43/containers/old1"} {
		f.Handle(p, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	}
	f.Handle("POST /v1.43/containers/create", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		createBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(201)
		io.WriteString(w, `{"Id":"new1"}`)
	})
	f.Handle("POST /v1.43/networks/{net}/connect", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		connect = append(connect, r.PathValue("net")+" "+string(b))
		mu.Unlock()
		w.WriteHeader(200)
	})
	if _, err := f.Client().Recreate(context.Background(), "old1", "", nil); err != nil {
		t.Fatal(err)
	}
	inOrder(t, f.Calls(), []string{"POST /v1.43/containers/create", "POST /v1.43/networks/backend/connect", "POST /v1.43/containers/new1/start"})

	var body struct {
		Image, User, WorkingDir, Hostname string
		Entrypoint, Cmd                   []string
		HostConfig                        struct {
			Memory, NanoCpus   int64
			CapAdd, ExtraHosts []string
			NetworkMode        string
		}
		NetworkingConfig struct {
			EndpointsConfig map[string]struct{ Aliases []string }
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if err := json.Unmarshal(createBody, &body); err != nil {
		t.Fatalf("%v: %s", err, createBody)
	}
	if body.Image != "myapp:1" || body.User != "1000:1000" || body.WorkingDir != "/srv" || body.Hostname != "" {
		t.Errorf("config fields: %+v", body)
	}
	if !reflect.DeepEqual(body.Entrypoint, []string{"/docker-entrypoint.sh"}) || !reflect.DeepEqual(body.Cmd, []string{"nginx", "-g", "daemon off;"}) {
		t.Errorf("entrypoint/cmd: %q %q", body.Entrypoint, body.Cmd)
	}
	hc := body.HostConfig
	if hc.Memory != 268435456 || hc.NanoCpus != 500000000 || !reflect.DeepEqual(hc.CapAdd, []string{"NET_ADMIN"}) ||
		!reflect.DeepEqual(hc.ExtraHosts, []string{"db.local:10.0.0.2"}) || hc.NetworkMode != "appnet" {
		t.Errorf("host config: %+v", hc)
	}
	eps := body.NetworkingConfig.EndpointsConfig
	if len(eps) != 1 || !reflect.DeepEqual(eps["appnet"].Aliases, []string{"api"}) {
		t.Errorf("endpoints: %+v", eps)
	}
	if len(connect) != 1 || !strings.Contains(connect[0], `"Container":"new1"`) || !strings.Contains(connect[0], `"Aliases":["svc"]`) {
		t.Errorf("connect: %v", connect)
	}
}
