package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNetworkBuiltinNoRemove(t *testing.T) {
	_, f, _, h := newTestServer(t)
	f.Handle("GET /v1.43/networks", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Id":"n1","Name":"bridge","Driver":"bridge"},{"Id":"n2","Name":"appnet","Driver":"bridge"}]`)
	})
	body := get(t, h, "/networks").Body.String()
	if strings.Contains(body, "/networks/bridge/remove") || strings.Contains(body, "/networks/n1/remove") {
		t.Errorf("builtin network has remove form:\n%s", body)
	}
	if !strings.Contains(body, "/networks/appnet/remove") || !strings.Contains(body, "data-confirm") {
		t.Errorf("custom network lacks confirm remove form:\n%s", body)
	}
}

func TestImagesAndVolumes(t *testing.T) {
	_, f, _, h := newTestServer(t)
	handleList(f)
	f.Handle("GET /v1.43/images/json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"Id":"sha256:aaa","RepoTags":["nginx:1"],"Size":1048576,"Created":1700000000}]`)
	})
	f.Handle("GET /v1.43/volumes", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Volumes":[{"Name":"data","Driver":"local","Mountpoint":"/x"}]}`)
	})
	if body := get(t, h, "/images").Body.String(); !strings.Contains(body, "nginx:1") || !strings.Contains(body, "1.0 MiB") || !strings.Contains(body, `action="/images/pull"`) {
		t.Errorf("images page:\n%s", body)
	}
	if body := get(t, h, "/volumes").Body.String(); !strings.Contains(body, "/volumes/data/remove") {
		t.Errorf("volumes page:\n%s", body)
	}
}

func TestResourceRemoveAndPrune(t *testing.T) {
	_, f, _, h := newTestServer(t)
	f.Handle("DELETE /v1.43/volumes/data", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("DELETE /v1.43/networks/appnet", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	f.Handle("DELETE /v1.43/images/{id}", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) })
	f.Handle("POST /v1.43/volumes/prune", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	c, csrf := login(t, h)
	for path, loc := range map[string]string{
		"/volumes/data/remove":      "/volumes",
		"/networks/appnet/remove":   "/networks",
		"/images/sha256:aaa/remove": "/images",
		"/volumes/prune":            "/volumes",
	} {
		rec := request(t, h, "POST", path, url.Values{}, c, csrf)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != loc {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestImagePullStream(t *testing.T) {
	_, f, _, h := newTestServer(t)
	f.Handle("POST /v1.43/images/create", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"Pulling","id":"abc"}`) })
	c, csrf := login(t, h)
	rec := request(t, h, "POST", "/images/pull", url.Values{"image": {"nginx:1"}}, c, csrf)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Pulling abc") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, "POST", "/images/pull", url.Values{"image": {""}}, c, csrf); rec.Code != 400 {
		t.Errorf("empty image status %d", rec.Code)
	}
}
