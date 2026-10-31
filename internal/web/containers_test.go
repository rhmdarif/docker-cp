package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"docker-cp/internal/docker/dockertest"
	"docker-cp/internal/store"
)

const listJSON = `[{"Id":"abc","Names":["/klien-a"],"Image":"nginx:1","State":"running","Status":"Up 1 hour","Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8001,"Type":"tcp"}],"Labels":{"com.docker.compose.project":"web"}}]`

const inspectJSON = `{"Id":"abc","Name":"/klien-a","State":{"Status":"running"},"Config":{"Image":"nginx:1","Tty":false,"Cmd":["nginx","-g","daemon off;"]},` +
	`"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"},"NetworkMode":"default","PortBindings":{"80/tcp":[{"HostIp":"","HostPort":"8001"}]}},` +
	`"Mounts":[{"Type":"volume","Name":"data","Source":"/var/lib/docker/volumes/data/_data","Destination":"/data","RW":true},` +
	`{"Type":"bind","Source":"/srv/x","Destination":"/x","RW":true}]}`

func noneListening(int) bool { return false }

func handleList(f *dockertest.Fake) {
	f.Handle("GET /v1.43/containers/json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, listJSON) })
	f.Handle("GET /v1.43/containers/klien-a/json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, inspectJSON) })
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	c, _ := login(t, h)
	return request(t, h, "GET", target, nil, c, "")
}

func TestContainersList(t *testing.T) {
	_, f, _, h := newTestServer(t)
	handleList(f)
	rec := get(t, h, "/containers")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "klien-a") || !strings.Contains(body, "8001") {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
}

func TestNewFromDuplicate(t *testing.T) {
	srv, f, _, h := newTestServer(t)
	srv.listening = noneListening
	handleList(f)
	f.Handle("GET /v1.43/containers/abc/json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, inspectJSON) })
	rec := get(t, h, "/containers/new?from=abc")
	body := rec.Body.String()
	for _, want := range []string{`value="klien-a-2"`, "8002:80", "data-klien-a-2", "bind mount dipakai bersama sumber", `nginx -g &#39;daemon off;&#39;`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestNewSuggestsPort(t *testing.T) {
	srv, f, _, h := newTestServer(t)
	srv.listening = noneListening
	handleList(f)
	body := get(t, h, "/containers/new").Body.String()
	if !strings.Contains(body, "8002:80") {
		t.Fatalf("expected 8002:80 suggestion (8001 used):\n%s", body)
	}
}

type createRecorder struct {
	mu   sync.Mutex
	body string
}

func fakeCreateFlow(f *dockertest.Fake) *createRecorder {
	rec := &createRecorder{}
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"Pulling"}`) })
	f.Handle("POST /v1.43/containers/create", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.body = string(b)
		rec.mu.Unlock()
		w.WriteHeader(201)
		io.WriteString(w, `{"Id":"new1"}`)
	})
	f.Handle("POST /v1.43/containers/new1/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	return rec
}

func TestCreateContainer(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	srv.listening = noneListening
	rec := fakeCreateFlow(f)
	c, csrf := login(t, h)
	form := url.Values{"name": {"app1"}, "image": {"nginx:1"}, "ports": {"8100:80"}, "env": {"A=1"}, "mounts": {"data:/data"}, "restart": {"always"}}
	resp := request(t, h, "POST", "/containers", form, c, csrf)
	if resp.Code != 200 || !strings.HasPrefix(resp.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d ct %q body %s", resp.Code, resp.Header().Get("Content-Type"), resp.Body.String())
	}
	inOrder(t, f.Calls(), []string{"POST /v1.43/images/create", "POST /v1.43/containers/create", "POST /v1.43/containers/new1/start"})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !strings.Contains(rec.body, `"HostIp":"127.0.0.1"`) {
		t.Errorf("create body lacks loopback HostIp: %s", rec.body)
	}
	loads := 0
	for _, c := range fc.Calls() {
		if c == "POST /load" {
			loads++
		}
	}
	if loads != 1 {
		t.Errorf("caddy /load calls = %d, calls %v", loads, fc.Calls())
	}
}

func TestCreateQuotedCmd(t *testing.T) {
	_, f, _, h := newTestServer(t)
	rec := fakeCreateFlow(f)
	c, csrf := login(t, h)
	form := url.Values{"name": {"web1"}, "image": {"nginx"}, "cmd": {`nginx -g 'daemon off;'`}}
	request(t, h, "POST", "/containers", form, c, csrf)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !strings.Contains(rec.body, `"Cmd":["nginx","-g","daemon off;"]`) {
		t.Fatalf("create body %s", rec.body)
	}
	if resp := request(t, h, "POST", "/containers", url.Values{"name": {"web2"}, "image": {"nginx"}, "cmd": {`sh -c "x`}}, c, csrf); resp.Code != http.StatusBadRequest {
		t.Fatalf("unterminated quote: status %d", resp.Code)
	}
}

func TestCreatePublishAll(t *testing.T) {
	_, f, _, h := newTestServer(t)
	rec := fakeCreateFlow(f)
	c, csrf := login(t, h)
	form := url.Values{"name": {"app1"}, "image": {"nginx"}, "ports": {"8100:80"}, "publish_all": {"on"}}
	resp := request(t, h, "POST", "/containers", form, c, csrf)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !strings.Contains(rec.body, `"HostIp":"0.0.0.0"`) || !strings.Contains(resp.Body.String(), "ufw") {
		t.Fatalf("create body %s\nresponse %s", rec.body, resp.Body.String())
	}
}

func TestCreateInvalidForm(t *testing.T) {
	_, f, _, h := newTestServer(t)
	fakeCreateFlow(f)
	c, csrf := login(t, h)
	for _, form := range []url.Values{
		{"name": {"app1"}, "image": {"nginx-keep"}, "ports": {"abc"}},
		{"name": {"-bad"}, "image": {"nginx-keep"}},
		{"name": {"app1"}, "image": {""}},
	} {
		resp := request(t, h, "POST", "/containers", form, c, csrf)
		if resp.Code != http.StatusBadRequest {
			t.Errorf("form %v: status %d", form, resp.Code)
		}
		body := resp.Body.String()
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="`+form.Get("name")+`"`) {
			t.Errorf("form %v: no error banner or refilled name:\n%s", form, body)
		}
		if form.Get("image") != "" && !strings.Contains(body, `value="nginx-keep"`) {
			t.Errorf("form %v: image not refilled", form)
		}
	}
	body := request(t, h, "POST", "/containers", url.Values{"name": {"app1"}, "image": {"nginx-keep"}, "ports": {"abc"}}, c, csrf).Body.String()
	if !strings.Contains(body, "format port harus HOST:CONTAINER") {
		t.Errorf("specific port error missing:\n%s", body)
	}
	for _, call := range f.Calls() {
		if strings.Contains(call, "/containers/create") || strings.Contains(call, "/images/create") {
			t.Errorf("unexpected docker call %s", call)
		}
	}
}

func TestContainerActionRow(t *testing.T) {
	_, f, _, h := newTestServer(t)
	handleList(f)
	f.Handle("POST /v1.43/containers/abc/restart", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	c, csrf := login(t, h)
	resp := request(t, h, "POST", "/containers/abc/restart", url.Values{}, c, csrf)
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "klien-a") || strings.Contains(resp.Body.String(), "<html") {
		t.Fatalf("status %d body %s", resp.Code, resp.Body.String())
	}
	if resp := request(t, h, "POST", "/containers/abc/explode", url.Values{}, c, csrf); resp.Code != 404 {
		t.Errorf("unknown action status %d", resp.Code)
	}
}

func TestRemoveAppliesCaddy(t *testing.T) {
	_, f, fc, h := newTestServer(t)
	f.Handle("DELETE /v1.43/containers/abc", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") == "" {
			t.Errorf("volumes=on should pass v: %v", r.URL.Query())
		}
		w.WriteHeader(204)
	})
	handleList(f)
	c, csrf := login(t, h)
	resp := request(t, h, "POST", "/containers/abc/remove", url.Values{"volumes": {"on"}}, c, csrf)
	if resp.Code != 200 {
		t.Fatalf("status %d %s", resp.Code, resp.Body.String())
	}
	if calls := f.Calls(); !contains(calls, "DELETE /v1.43/containers/abc") {
		t.Fatalf("calls %v", calls)
	}
	if !contains(fc.Calls(), "POST /load") {
		t.Fatalf("caddy calls %v", fc.Calls())
	}
}

func TestStartRestartApplyCaddy(t *testing.T) {
	srv, f, fc, h := newTestServer(t)
	handleList(f)
	f.Handle("POST /v1.43/containers/abc/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("POST /v1.43/containers/abc/restart", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	c, csrf := login(t, h)
	// No container-backed site: Caddy is left alone.
	request(t, h, "POST", "/containers/abc/start", url.Values{}, c, csrf)
	if contains(fc.Calls(), "POST /load") {
		t.Fatalf("unexpected caddy load %v", fc.Calls())
	}
	srv.d.Store.Update(func(st *store.State) error {
		st.Sites = []store.Site{{ID: "s1", Domains: []string{"a.example.com"}, Target: store.Target{Container: "klien-a", ContainerPort: 80}}}
		return nil
	})
	for _, action := range []string{"start", "restart"} {
		before := len(fc.Calls())
		resp := request(t, h, "POST", "/containers/abc/"+action, url.Values{}, c, csrf)
		if resp.Code != 200 || !contains(fc.Calls()[before:], "POST /load") {
			t.Fatalf("%s: status %d caddy calls %v", action, resp.Code, fc.Calls())
		}
		if !strings.Contains(fc.LastLoad(), "reverse_proxy 127.0.0.1:8001") {
			t.Fatalf("%s: loaded %q", action, fc.LastLoad())
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestRecreateStream(t *testing.T) {
	_, f, fc, h := newTestServer(t)
	f.Handle("GET /v1.43/containers/old1/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Id":"old1","Name":"/app","State":{"Status":"running"},"Config":{"Image":"myapp:1"},"HostConfig":{"RestartPolicy":{"Name":"always"},"NetworkMode":"default"}}`)
	})
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"Done"}`) })
	f.Handle("POST /v1.43/containers/old1/stop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("POST /v1.43/containers/old1/rename", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("POST /v1.43/containers/create", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); io.WriteString(w, `{"Id":"new1"}`) })
	f.Handle("POST /v1.43/containers/new1/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("DELETE /v1.43/containers/old1", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	c, csrf := login(t, h)
	resp := request(t, h, "POST", "/containers/old1/recreate", url.Values{}, c, csrf)
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "Selesai") {
		t.Fatalf("status %d body %s", resp.Code, resp.Body.String())
	}
	if !contains(fc.Calls(), "POST /load") {
		t.Fatalf("caddy calls %v", fc.Calls())
	}
}

func TestRecreateRejectsCompose(t *testing.T) {
	_, f, _, h := newTestServer(t)
	handleList(f)
	f.Handle("GET /v1.43/containers/abc/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Id":"abc","Name":"/klien-a","Config":{"Image":"nginx:1","Labels":{"com.docker.compose.project":"web"}},"HostConfig":{}}`)
	})
	c, csrf := login(t, h)
	resp := request(t, h, "POST", "/containers/abc/recreate", url.Values{}, c, csrf)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "Stacks") {
		t.Fatalf("status %d body %s", resp.Code, resp.Body.String())
	}
	for _, call := range f.Calls() {
		if strings.Contains(call, "images/create") || strings.Contains(call, "/stop") {
			t.Fatalf("unexpected docker call %s", call)
		}
	}
	body := get(t, h, "/containers").Body.String()
	if strings.Contains(body, "/containers/abc/recreate") || !strings.Contains(body, "Pull &amp; redeploy") {
		t.Fatalf("compose row offers recreate:\n%s", body)
	}
}

func frame(s string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = 1
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(s)))
	return append(hdr, s...)
}

func fakeInspectOnly(f *dockertest.Fake) {
	f.Handle("GET /v1.43/containers/abc/json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, inspectJSON) })
}

func TestLogsSSE(t *testing.T) {
	_, f, _, h := newTestServer(t)
	fakeInspectOnly(f)
	f.Handle("GET /v1.43/containers/abc/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tail") != "50" {
			t.Errorf("tail = %q", r.URL.Query().Get("tail"))
		}
		w.Write(frame("baris1\r\n"))
		w.Write(frame("baris2\n"))
	})
	rec := get(t, h, "/containers/abc/logs/stream?tail=50")
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type %q", ct)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("headers %v", rec.Header())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: baris1\n\n") || !strings.Contains(body, "data: baris2\n\n") || strings.Contains(body, "\r") {
		t.Fatalf("body %q", body)
	}
}

func TestLogsSSEClosesOnDisconnect(t *testing.T) {
	_, f, _, h := newTestServer(t)
	fakeInspectOnly(f)
	closed := make(chan struct{})
	f.Handle("GET /v1.43/containers/abc/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame("hello\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	c, _ := login(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/containers/abc/logs/stream", nil).WithContext(ctx)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("docker log connection not closed within 2s of disconnect")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return")
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("data: hello")) {
		t.Errorf("body %q", rec.Body.String())
	}
}

func TestLogsPostNotAllowed(t *testing.T) {
	_, _, _, h := newTestServer(t)
	c, csrf := login(t, h)
	if rec := request(t, h, "POST", "/containers/abc/logs/stream", url.Values{}, c, csrf); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestStatsFragment(t *testing.T) {
	_, f, _, h := newTestServer(t)
	f.Handle("GET /v1.43/containers/abc/stats", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"cpu_stats":{"cpu_usage":{"total_usage":200},"system_cpu_usage":2000,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},"memory_stats":{"usage":104857600,"limit":1073741824,"stats":{}}}`)
	})
	rec := get(t, h, "/containers/abc/stats")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "20.0%") || !strings.Contains(body, "100.0 MiB") || strings.Contains(body, "<html") {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
}

// inOrder fails unless want appears in calls as an ordered subsequence.
func inOrder(t *testing.T, calls, want []string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("calls %v do not contain %v in order", calls, want)
	}
}
