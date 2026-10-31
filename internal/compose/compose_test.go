package compose

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"docker-cp/internal/docker"
	"docker-cp/internal/docker/dockertest"
)

func newMgr(t *testing.T) (*Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("FAKE_LOG", log)
	bin, err := filepath.Abs("testdata/fakedocker.sh")
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(dir, bin), dir, log
}

func logLines(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"shop", "a_b-1"} {
		if !ValidName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range []string{"", "../x", "Shop", "a/b", "-a", strings.Repeat("a", 64)} {
		if ValidName(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
	m, _, _ := newMgr(t)
	if err := m.Write("../x", "a", ""); err == nil {
		t.Error("Write with bad name must fail")
	}
	if _, _, err := m.Read("../x"); err == nil {
		t.Error("Read with bad name must fail")
	}
	if err := m.Run(context.Background(), "../x", "up", nil); err == nil {
		t.Error("Run with bad name must fail")
	}
	if err := m.Delete(context.Background(), "../x", nil); err == nil {
		t.Error("Delete with bad name must fail")
	}
}

func TestWriteRead(t *testing.T) {
	m, dir, _ := newMgr(t)
	if err := m.Write("shop", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	c, e, err := m.Read("shop")
	if err != nil || c != "services: {}\n" || e != "A=1\n" {
		t.Fatalf("got %q %q %v", c, e, err)
	}
	for p, want := range map[string]os.FileMode{"shop": 0o700, "shop/.env": 0o600, "shop/docker-compose.yml": 0o644} {
		st, err := os.Stat(filepath.Join(dir, p))
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v", p, st, err)
		}
	}
	if err := m.Write("shop", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shop", ".env")); !os.IsNotExist(err) {
		t.Errorf(".env should be removed: %v", err)
	}
	if _, e, _ := m.Read("shop"); e != "" {
		t.Errorf("env=%q", e)
	}
}

const upArgs = "up -d --remove-orphans"

func prefix(dir string) string {
	d := filepath.Join(dir, "shop")
	return "compose --project-directory " + d + " -p shop -f " + filepath.Join(d, "docker-compose.yml") + " "
}

func TestRunUpArgs(t *testing.T) {
	m, dir, log := newMgr(t)
	if err := m.Write("shop", "x", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Run(context.Background(), "shop", "up", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{prefix(dir) + upArgs}
	if got := logLines(t, log); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestRunPullThenUp(t *testing.T) {
	m, dir, log := newMgr(t)
	m.Write("shop", "x", "")
	if err := m.Run(context.Background(), "shop", "pull", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{prefix(dir) + "pull", prefix(dir) + upArgs}
	if got := logLines(t, log); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestRunStopAndOutput(t *testing.T) {
	m, dir, log := newMgr(t)
	m.Write("shop", "x", "")
	if err := m.Run(context.Background(), "shop", "stop", nil); err != nil {
		t.Fatal(err)
	}
	if got := logLines(t, log); !reflect.DeepEqual(got, []string{prefix(dir) + "stop"}) {
		t.Errorf("got %v", got)
	}
	if err := m.Run(context.Background(), "shop", "bogus", nil); err == nil {
		t.Error("unknown action must fail")
	}
}

func TestRunFailure(t *testing.T) {
	m, _, _ := newMgr(t)
	m.Write("shop", "x", "")
	t.Setenv("FAKE_EXIT", "3")
	err := m.Run(context.Background(), "shop", "up", nil)
	if err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "docker compose up gagal") {
		t.Errorf("err=%v", err)
	}
}

func TestDelete(t *testing.T) {
	m, dir, log := newMgr(t)
	m.Write("shop", "x", "A=1")
	if err := m.Delete(context.Background(), "shop", nil); err != nil {
		t.Fatal(err)
	}
	if got := logLines(t, log); !reflect.DeepEqual(got, []string{prefix(dir) + "down"}) {
		t.Errorf("got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "shop")); !os.IsNotExist(err) {
		t.Errorf("dir still exists: %v", err)
	}
}

func TestDeleteKeepsDirOnFailure(t *testing.T) {
	m, dir, _ := newMgr(t)
	m.Write("shop", "x", "")
	t.Setenv("FAKE_EXIT", "1")
	if err := m.Delete(context.Background(), "shop", nil); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "shop")); err != nil {
		t.Errorf("dir must remain: %v", err)
	}
}

func TestListMergesExternal(t *testing.T) {
	m, dir, _ := newMgr(t)
	os.Mkdir(filepath.Join(dir, "shop"), 0o700)
	os.Mkdir(filepath.Join(dir, "empty"), 0o700)
	cs := []docker.ContainerSummary{
		{ID: "a", Project: "shop", State: "running"},
		{ID: "b", Project: "shop", State: "exited"},
		{ID: "c", Project: "legacy", State: "running"},
		{ID: "d"},
	}
	got, err := m.List(cs)
	if err != nil {
		t.Fatal(err)
	}
	want := []Stack{
		{"empty", true, 0, 0},
		{"legacy", false, 1, 1},
		{"shop", true, 2, 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestListMissingDir(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "nope"), "docker")
	got, err := m.List(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v %v", got, err)
	}
}

func TestStartProject(t *testing.T) {
	f := dockertest.New(t)
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
	f.Handle("POST /v1.43/containers/a/start", ok)
	f.Handle("POST /v1.43/containers/b/start", ok)
	f.Handle("POST /v1.43/containers/a/stop", ok)
	f.Handle("POST /v1.43/containers/b/stop", ok)
	cs := []docker.ContainerSummary{
		{ID: "a", Project: "shop"}, {ID: "b", Project: "shop"}, {ID: "c", Project: "other"},
	}
	if err := StartProject(context.Background(), f.Client(), cs, "shop"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1.43/containers/a/start", "POST /v1.43/containers/b/start"}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls=%v", got)
	}
	if err := StopProject(context.Background(), f.Client(), cs, "shop"); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()[2:]; !reflect.DeepEqual(got, []string{"POST /v1.43/containers/a/stop", "POST /v1.43/containers/b/stop"}) {
		t.Errorf("calls=%v", got)
	}
	if err := StartProject(context.Background(), f.Client(), cs, "nothing"); err == nil {
		t.Error("unknown project must fail")
	}
}
