package terminal

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startHost serves ServeHost over a test server and returns a connected client.
func startHost(t *testing.T, onStart func(pid int)) (*websocket.Conn, context.Context, <-chan error) {
	t.Helper()
	errc := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			errc <- err
			return
		}
		errc <- ServeHost(r.Context(), ws, "/bin/sh", onStart)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx, errc
}

// readUntil collects binary output until it contains want or the deadline passes.
func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var buf bytes.Buffer
	for !strings.Contains(buf.String(), want) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, buf.String(), err)
		}
		buf.Write(data)
	}
	return buf.String()
}

func TestServeHostEcho(t *testing.T) {
	c, ctx, _ := startHost(t, nil)
	if err := c.Write(ctx, websocket.MessageBinary, []byte("echo hi-$((1+1))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "hi-2")
}

func TestServeHostResize(t *testing.T) {
	c, ctx, _ := startHost(t, nil)
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.Write(ctx, websocket.MessageBinary, []byte("stty size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "40 100")
}

func TestServeHostIgnoresBadControl(t *testing.T) {
	c, ctx, _ := startHost(t, nil)
	for _, m := range []string{`not json`, `{"type":"bogus"}`} {
		if err := c.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("echo ok-$((2+3))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "ok-5")
}

func TestServeHostKillsOnClose(t *testing.T) {
	pidc := make(chan int, 1)
	c, _, errc := startHost(t, func(pid int) { pidc <- pid })
	pid := <-pidc
	c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHost did not return")
	}
}

func TestServeHostClosesWhenShellExits(t *testing.T) {
	c, ctx, errc := startHost(t, nil)
	if err := c.Write(ctx, websocket.MessageBinary, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(rctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("want normal closure, got %v", err)
			}
			break
		}
	}
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHost did not return")
	}
}

type sig struct {
	pid int
	s   syscall.Signal
}

func TestKillExec(t *testing.T) {
	old := execPoll
	execPoll = 10 * time.Millisecond
	defer func() { execPoll = old }()

	run := func(states []bool, inspectErr error) []sig {
		var sent []sig
		i := 0
		inspect := func(ctx context.Context, id string) (int, bool, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("inspect without deadline")
			}
			r := states[min(i, len(states)-1)]
			i++
			return 77, r, inspectErr
		}
		kill := func(pid int, s syscall.Signal) error { sent = append(sent, sig{pid, s}); return nil }
		killExec(inspect, kill, "e1", 50*time.Millisecond)
		return sent
	}
	if got := run([]bool{false}, nil); len(got) != 0 {
		t.Errorf("exited exec: sent %v", got)
	}
	if got := run([]bool{true}, context.DeadlineExceeded); len(got) != 0 {
		t.Errorf("inspect error: sent %v", got)
	}
	if got := run([]bool{true, true, false}, nil); len(got) != 1 || got[0] != (sig{77, syscall.SIGHUP}) {
		t.Errorf("exits after HUP: sent %v", got)
	}
	if got := run([]bool{true}, nil); len(got) != 2 || got[0] != (sig{77, syscall.SIGHUP}) || got[1] != (sig{77, syscall.SIGKILL}) {
		t.Errorf("ignores HUP: sent %v", got)
	}
}
