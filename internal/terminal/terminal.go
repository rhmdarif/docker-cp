// Package terminal bridges a browser WebSocket to a host PTY shell or a
// Docker exec session.
//
// Protocol: binary messages are raw terminal data in both directions; text
// messages from the client are JSON control messages, currently only
// {"type":"resize","cols":N,"rows":N}. Unknown or malformed control messages
// are ignored.
package terminal

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"docker-cp/internal/docker"
	"docker-cp/internal/procenv"
)

// ExecCmd is run via ["/bin/sh","-c",ExecCmd] inside containers.
const ExecCmd = `command -v bash >/dev/null 2>&1 && exec bash || exec sh`

const (
	readLimit   = 1 << 20
	copyBufSize = 32 << 10
	killGrace   = 2 * time.Second
)

// DefaultShell returns /bin/bash when present, otherwise /bin/sh.
func DefaultShell() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

type control struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// ServeHost runs `shell -l` on a PTY and bridges it to ws until either side
// ends. onStart (may be nil) receives the shell pid.
func ServeHost(ctx context.Context, ws *websocket.Conn, shell string, onStart func(pid int)) error {
	cmd := exec.Command(shell, "-l")
	cmd.Env = procenv.Child("TERM=xterm-256color")
	if os.Geteuid() == 0 {
		cmd.Env = append(cmd.Env, "HOME=/root")
	}
	ptmx, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		ws.Close(websocket.StatusInternalError, "cannot start shell")
		return err
	}
	pid := cmd.Process.Pid
	if onStart != nil {
		onStart(pid)
	}

	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	resize := func(rows, cols uint16) {
		pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	}
	teardown := func() {
		select {
		case <-exited:
		default:
			syscall.Kill(-pid, syscall.SIGHUP)
			select {
			case <-exited:
			case <-time.After(killGrace):
				syscall.Kill(-pid, syscall.SIGKILL)
				<-exited
			}
		}
		ptmx.Close()
	}
	bridge(ctx, ws, ptmx, resize, teardown)
	return nil
}

// ServeExec attaches a TTY exec session in the container and bridges it to ws.
func ServeExec(ctx context.Context, ws *websocket.Conn, dc *docker.Client, containerID string) error {
	sess, err := dc.ExecAttach(ctx, containerID, []string{"/bin/sh", "-c", ExecCmd})
	if err != nil {
		ws.Close(websocket.StatusInternalError, "cannot attach exec")
		return err
	}
	resize := func(rows, cols uint16) {
		dc.ExecResize(ctx, sess.ID, rows, cols)
	}
	bridge(ctx, ws, sess.Conn, resize, func() {
		sess.Conn.Close()
		// Docker does not end an exec when its attach connection closes.
		killExec(dc.ExecInspect, syscall.Kill, sess.ID, killGrace)
	})
	return nil
}

// execPoll is how often killExec re-checks an exec after SIGHUP.
var execPoll = 100 * time.Millisecond

// killExec ends a still-running exec: SIGHUP to its host pid, then SIGKILL if
// it is still running after grace. It uses its own short contexts because the
// request context is usually already cancelled at teardown.
func killExec(inspect func(ctx context.Context, execID string) (int, bool, error), kill func(pid int, s syscall.Signal) error, execID string, grace time.Duration) {
	running := func() (int, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pid, ok, err := inspect(ctx, execID)
		return pid, err == nil && ok && pid > 0
	}
	pid, ok := running()
	if !ok {
		return
	}
	kill(pid, syscall.SIGHUP)
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		time.Sleep(execPoll)
		if _, ok := running(); !ok {
			return
		}
	}
	kill(pid, syscall.SIGKILL)
}

// bridge copies between ws and rw until either side ends or ctx is done, then
// runs teardown (which must make rw reads fail), closes ws normally and waits
// for both copy goroutines.
func bridge(ctx context.Context, ws *websocket.Conn, rw io.ReadWriter, resize func(rows, cols uint16), teardown func()) {
	ws.SetReadLimit(readLimit)
	stop := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(stop) }) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // rw -> ws
		defer wg.Done()
		defer finish()
		buf := make([]byte, copyBufSize)
		for {
			n, err := rw.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { // ws -> rw
		defer wg.Done()
		defer finish()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := rw.Write(data); err != nil {
					return
				}
				continue
			}
			var c control
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" && c.Rows > 0 && c.Cols > 0 {
				resize(c.Rows, c.Cols)
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-stop:
	}
	teardown()
	ws.Close(websocket.StatusNormalClosure, "")
	wg.Wait()
}
