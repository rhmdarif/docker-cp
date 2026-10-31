package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func wsDial(t *testing.T, base, path string, cookie *http.Cookie, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if cookie != nil {
		hdr.Set("Cookie", cookie.Name+"="+cookie.Value)
	}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+path, &websocket.DialOptions{HTTPHeader: hdr})
}

func TestWSHostRequiresAuth(t *testing.T) {
	_, _, _, h := newTestServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	_, resp, err := wsDial(t, ts.URL, "/ws/host", nil, ts.URL)
	if err == nil {
		t.Fatal("dial without cookie succeeded")
	}
	if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatalf("resp = %v", resp)
	}
}

func TestWSHostOriginRejected(t *testing.T) {
	_, _, _, h := newTestServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	cookie, _ := login(t, h)
	_, resp, err := wsDial(t, ts.URL, "/ws/host", cookie, "http://evil")
	if err == nil {
		t.Fatal("dial with foreign origin succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resp = %v, want 403", resp)
	}
}

func TestWSExecRequiresOrigin(t *testing.T) {
	_, _, _, h := newTestServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	cookie, _ := login(t, h)
	_, resp, err := wsDial(t, ts.URL, "/ws/exec/abc", cookie, "http://evil")
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("err=%v resp=%v, want 403", err, resp)
	}
}

func TestWSHostEcho(t *testing.T) {
	_, _, _, h := newTestServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	cookie, _ := login(t, h)
	c, _, err := wsDial(t, ts.URL, "/ws/host", cookie, ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("echo ok-$((2+2))\n")); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for !strings.Contains(got.String(), "ok-4") {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got.String())
		}
		got.Write(b)
	}
}

func TestTerminalPageRenders(t *testing.T) {
	_, _, _, h := newTestServer(t)
	cookie, _ := login(t, h)
	rec := request(t, h, "GET", "/terminal", nil, cookie, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "xterm.js") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, "GET", "/containers/abc123/terminal", nil, cookie, "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "xterm.js") || !strings.Contains(body, `data-ws="/ws/exec/abc123"`) {
		t.Fatalf("%d %s", rec.Code, body)
	}
}
