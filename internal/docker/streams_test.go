package docker_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"docker-cp/internal/docker"
	"docker-cp/internal/docker/dockertest"
)

func TestDemuxLogs(t *testing.T) {
	in := []byte{1, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c', 2, 0, 0, 0, 0, 0, 0, 2, 'e', '\n'}
	var out bytes.Buffer
	if err := docker.DemuxLogs(bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "abce\n" {
		t.Fatalf("out=%q", out.String())
	}
}

func TestDemuxLogsTruncated(t *testing.T) {
	in := []byte{1, 0, 0, 0, 0, 0, 0, 5, 'a'}
	if err := docker.DemuxLogs(bytes.NewReader(in), io.Discard); err == nil {
		t.Fatal("want error on truncated frame")
	}
}

func TestLogsQuery(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/c1/logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("stdout") != "1" || q.Get("stderr") != "1" || q.Get("tail") != "50" || q.Get("follow") != "1" || q.Get("timestamps") != "0" {
			t.Errorf("query=%v", q)
		}
		io.WriteString(w, "x")
	})
	rc, err := f.Client().Logs(context.Background(), "c1", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "x" {
		t.Fatalf("b=%q", b)
	}
}

func TestStatsOnce(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/c1/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" {
			t.Errorf("stream=%q", r.URL.Query().Get("stream"))
		}
		io.WriteString(w, `{"cpu_stats":{"cpu_usage":{"total_usage":400},"system_cpu_usage":2000,"online_cpus":2},
"precpu_stats":{"cpu_usage":{"total_usage":200},"system_cpu_usage":1000},
"memory_stats":{"usage":104857600,"limit":1073741824,"stats":{"inactive_file":20971520}}}`)
	})
	s, err := f.Client().StatsOnce(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUPercent != 40 || s.MemUsage != 80<<20 || s.MemLimit != 1<<30 {
		t.Fatalf("s=%+v", s)
	}
}

func TestStatsOnceZeroDelta(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("GET /v1.43/containers/c1/stats", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"memory_stats":{"usage":10,"stats":{"inactive_file":50}}}`)
	})
	s, err := f.Client().StatsOnce(context.Background(), "c1")
	if err != nil || s.CPUPercent != 0 || s.MemUsage != 0 {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}

func TestExecAttachUpgrade(t *testing.T) {
	f := dockertest.New(t)
	f.Handle("POST /v1.43/containers/c1/exec", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["Tty"] != true || b["AttachStdin"] != true {
			t.Errorf("body=%v", b)
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"Id":"e1"}`)
	})
	f.Handle("POST /v1.43/exec/e1/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "tcp" {
			t.Errorf("upgrade=%q", r.Header.Get("Upgrade"))
		}
		io.Copy(io.Discard, r.Body)
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\nready\n")
		line, _ := brw.ReadString('\n')
		io.WriteString(conn, "echo:"+line)
	})
	s, err := f.Client().ExecAttach(context.Background(), "c1", []string{"sh"})
	if err != nil || s.ID != "e1" {
		t.Fatalf("s=%v err=%v", s, err)
	}
	defer s.Conn.Close()
	br := bufio.NewReader(s.Conn)
	if l, _ := br.ReadString('\n'); l != "ready\n" {
		t.Fatalf("l=%q", l)
	}
	io.WriteString(s.Conn, "hi\n")
	if l, _ := br.ReadString('\n'); l != "echo:hi\n" {
		t.Fatalf("l=%q", l)
	}
}
