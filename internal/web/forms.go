package web

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"docker-cp/internal/docker"
)

var (
	envKeyRe        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)
)

// lines yields the non-empty trimmed lines of text with their 1-based number.
func lines(text string, fn func(line string) error) error {
	for i, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if err := fn(l); err != nil {
			return fmt.Errorf("baris %d: %w", i+1, err)
		}
	}
	return nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q tidak valid (1-65535)", s)
	}
	return n, nil
}

// ParsePorts parses one binding per line: "8001:80", "0.0.0.0:8001:80" or
// "8001:80/udp". Blank lines are ignored.
func ParsePorts(text string) ([]docker.PortBinding, error) {
	var out []docker.PortBinding
	err := lines(text, func(l string) error {
		spec, proto, hasProto := strings.Cut(l, "/")
		if !hasProto {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			return fmt.Errorf("protokol %q tidak valid", proto)
		}
		parts := strings.Split(spec, ":")
		var pb docker.PortBinding
		pb.Proto = proto
		switch len(parts) {
		case 2:
		case 3:
			if net.ParseIP(parts[0]) == nil {
				return fmt.Errorf("alamat IP %q tidak valid", parts[0])
			}
			pb.HostIP, parts = parts[0], parts[1:]
		default:
			return fmt.Errorf("format port harus HOST:CONTAINER, dapat %q", l)
		}
		var err error
		if pb.HostPort, err = parsePort(parts[0]); err != nil {
			return err
		}
		if pb.ContainerPort, err = parsePort(parts[1]); err != nil {
			return err
		}
		out = append(out, pb)
		return nil
	})
	return out, err
}

// FormatPorts is the inverse of ParsePorts.
func FormatPorts(ps []docker.PortBinding) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte('\n')
		}
		if p.HostIP != "" {
			b.WriteString(p.HostIP + ":")
		}
		fmt.Fprintf(&b, "%d:%d", p.HostPort, p.ContainerPort)
		if p.Proto != "" && p.Proto != "tcp" {
			b.WriteString("/" + p.Proto)
		}
	}
	return b.String()
}

// ParseEnv parses KEY=VALUE lines; lines starting with '#' are comments.
func ParseEnv(text string) ([]string, error) {
	var out []string
	err := lines(text, func(l string) error {
		if strings.HasPrefix(l, "#") {
			return nil
		}
		k, _, ok := strings.Cut(l, "=")
		if !ok || !envKeyRe.MatchString(k) {
			return fmt.Errorf("variabel harus berbentuk KEY=VALUE dengan KEY [A-Za-z_][A-Za-z0-9_]*, dapat %q", l)
		}
		out = append(out, l)
		return nil
	})
	return out, err
}

// FormatEnv is the inverse of ParseEnv.
func FormatEnv(env []string) string { return strings.Join(env, "\n") }

// ParseMounts parses "volume:/target" or "/host/path:/target[:ro]" lines. A
// source starting with "/" is a bind mount, anything else a named volume.
func ParseMounts(text string) ([]docker.Mount, error) {
	var out []docker.Mount
	err := lines(text, func(l string) error {
		parts := strings.Split(l, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return fmt.Errorf("mount harus berbentuk SUMBER:TARGET[:ro], dapat %q", l)
		}
		m := docker.Mount{Type: "volume", Source: parts[0], Target: parts[1]}
		if m.Source == "" {
			return fmt.Errorf("sumber mount kosong")
		}
		if !strings.HasPrefix(m.Target, "/") {
			return fmt.Errorf("target %q harus path absolut", m.Target)
		}
		if strings.HasPrefix(m.Source, "/") {
			m.Type = "bind"
		}
		if len(parts) == 3 {
			if parts[2] != "ro" {
				return fmt.Errorf("opsi mount %q tidak dikenal (hanya ro)", parts[2])
			}
			m.ReadOnly = true
		}
		out = append(out, m)
		return nil
	})
	return out, err
}

// FormatMounts is the inverse of ParseMounts.
func FormatMounts(ms []docker.Mount) string {
	var b strings.Builder
	for i, m := range ms {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Source + ":" + m.Target)
		if m.ReadOnly {
			b.WriteString(":ro")
		}
	}
	return b.String()
}

// FormatCmd renders an argv as one shell-style line: arguments made only of
// safe characters are written as-is, anything else is single-quoted.
func FormatCmd(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// ParseCmd splits a shell-style line into argv. It understands '...'
// (literal), "..." (backslash escapes \" \\ \$ and \`) and backslash escapes
// outside quotes. No variable expansion or globbing takes place.
func ParseCmd(s string) ([]string, error) {
	var (
		argv  []string
		cur   strings.Builder
		inTok bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inTok {
				argv = append(argv, cur.String())
				cur.Reset()
				inTok = false
			}
		case c == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("perintah diakhiri backslash")
			}
			i++
			cur.WriteByte(s[i])
			inTok = true
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("tanda kutip ' tidak ditutup")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inTok = true
		case c == '"':
			inTok = true
			closed := false
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, fmt.Errorf("tanda kutip \" tidak ditutup")
			}
		default:
			cur.WriteByte(c)
			inTok = true
		}
	}
	if inTok {
		argv = append(argv, cur.String())
	}
	return argv, nil
}
