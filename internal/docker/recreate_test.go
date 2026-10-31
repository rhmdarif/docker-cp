package docker_test

import (
	"context"
	"io"
	"net/http"
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
	if _, err := f.Client().Recreate(context.Background(), "old1", "", nil); err == nil {
		t.Fatal("want error")
	}
	inOrder(t, f.Calls(), []string{
		"POST /v1.43/containers/new1/start", "DELETE /v1.43/containers/new1",
		"POST /v1.43/containers/old1/rename", "POST /v1.43/containers/old1/start",
	})
	if len(*renames) != 2 || (*renames)[1] != "app" {
		t.Errorf("renames=%v", *renames)
	}
}
