//go:build integration

package docker_test

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"docker-cp/internal/docker"
)

func TestExecIntegration(t *testing.T) {
	const sock = "/var/run/docker.sock"
	if _, err := os.Stat(sock); err != nil {
		t.Skip("docker socket not present")
	}
	ctx := context.Background()
	c := docker.New(sock)
	if err := c.Pull(ctx, "alpine", "", nil); err != nil {
		t.Skip("cannot pull alpine: ", err)
	}
	id, err := c.Create(ctx, docker.CreateSpec{Image: "alpine", Cmd: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Remove(ctx, id, true)
	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	s, err := c.ExecAttach(ctx, id, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Conn.Close()
	if err := c.ExecResize(ctx, s.ID, 24, 80); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Conn.Write([]byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(s.Conn)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "hi" {
				done <- true
				return
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("never saw hi")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}
}
