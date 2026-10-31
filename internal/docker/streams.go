package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// Logs streams stdout+stderr of a container. With a non-TTY container the
// stream is multiplexed; pass it through DemuxLogs.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"0"}, "tail": {strconv.Itoa(tail)}}
	if follow {
		q.Set("follow", "1")
	} else {
		q.Set("follow", "0")
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+esc(id)+"/logs", q, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DemuxLogs copies the payload of a Docker multiplexed stream (8 byte frame
// headers) to w, merging stdout and stderr.
func DemuxLogs(r io.Reader, w io.Writer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(w, r, n); err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

// Stats is a point-in-time resource sample.
type Stats struct {
	CPUPercent         float64
	MemUsage, MemLimit uint64
}

// StatsOnce returns a single stats sample (stream=false).
func (c *Client) StatsOnce(ctx context.Context, id string) (Stats, error) {
	var r struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System     uint64 `json:"system_cpu_usage"`
			OnlineCPUs uint64 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		Mem struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
			Stats struct {
				InactiveFile uint64 `json:"inactive_file"`
			} `json:"stats"`
		} `json:"memory_stats"`
	}
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	if err := c.getJSON(ctx, "/containers/"+esc(id)+"/stats", q, &r); err != nil {
		return Stats{}, err
	}
	var s Stats
	cpuDelta := float64(r.CPU.Usage.Total) - float64(r.PreCPU.Usage.Total)
	sysDelta := float64(r.CPU.System) - float64(r.PreCPU.System)
	if sysDelta > 0 && cpuDelta > 0 {
		s.CPUPercent = cpuDelta / sysDelta * float64(r.CPU.OnlineCPUs) * 100
	}
	if r.Mem.Usage > r.Mem.Stats.InactiveFile {
		s.MemUsage = r.Mem.Usage - r.Mem.Stats.InactiveFile
	}
	s.MemLimit = r.Mem.Limit
	return s, nil
}

// ExecSession is an attached exec; Conn carries raw TTY bytes both ways.
type ExecSession struct {
	ID   string
	Conn io.ReadWriteCloser
}

type hijackedConn struct {
	r *bufio.Reader
	net.Conn
}

func (h *hijackedConn) Read(p []byte) (int, error) { return h.r.Read(p) }

// ExecAttach creates a TTY exec running cmd in the container and attaches to
// it over a hijacked (HTTP upgrade) connection.
func (c *Client) ExecAttach(ctx context.Context, containerID string, cmd []string) (*ExecSession, error) {
	body, _ := json.Marshal(map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true,
		"Tty": true, "Cmd": cmd, "Env": []string{"TERM=xterm-256color"},
	})
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+esc(containerID)+"/exec", nil, bytes.NewReader(body), nil)
	if err != nil {
		return nil, err
	}
	var created struct{ Id string }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if created.Id == "" {
		return nil, fmt.Errorf("docker: exec create returned no id")
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return nil, err
	}
	startBody := `{"Detach":false,"Tty":true}`
	req := fmt.Sprintf("POST /v1.43/exec/%s/start HTTP/1.1\r\nHost: docker\r\nContent-Type: application/json\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: %d\r\n\r\n%s",
		esc(created.Id), len(startBody), startBody)
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	hr, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if hr.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(hr.Body, 4096))
		hr.Body.Close()
		conn.Close()
		return nil, &APIError{Status: hr.StatusCode, Message: string(bytes.TrimSpace(b))}
	}
	return &ExecSession{ID: created.Id, Conn: &hijackedConn{r: br, Conn: conn}}, nil
}

// ExecResize sets the TTY size of an exec.
func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols uint16) error {
	q := url.Values{"h": {strconv.Itoa(int(rows))}, "w": {strconv.Itoa(int(cols))}}
	return c.call(ctx, http.MethodPost, "/exec/"+esc(execID)+"/resize", q)
}
