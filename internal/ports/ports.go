// Package ports suggests free host ports and names for new containers.
package ports

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"docker-cp/internal/docker"
)

// UsedPorts collects host ports published by cs plus extra (e.g. the panel port).
func UsedPorts(cs []docker.ContainerSummary, extra ...int) map[int]bool {
	used := map[int]bool{}
	for _, c := range cs {
		for _, p := range c.Ports {
			if p.HostPort > 0 {
				used[p.HostPort] = true
			}
		}
	}
	for _, p := range extra {
		used[p] = true
	}
	return used
}

// HostListening reports whether port cannot be bound on the host.
func HostListening(port int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// NextFree returns the first port >= start that is neither used nor listening,
// or 0 when none is left up to 65535.
func NextFree(start int, used map[int]bool, listening func(int) bool) int {
	if start < 1 {
		start = 1
	}
	for p := start; p <= 65535; p++ {
		if !used[p] && !listening(p) {
			return p
		}
	}
	return 0
}

// SuggestName returns base with a numeric suffix (-2, -3, ...) that does not exist.
// A base already ending in -N is incremented from N+1.
func SuggestName(base string, exists func(string) bool) string {
	stem, n := base, 2
	if i := strings.LastIndex(base, "-"); i > 0 {
		if v, err := strconv.Atoi(base[i+1:]); err == nil && v >= 0 {
			stem, n = base[:i], v+1
		}
	}
	if stem == base && !exists(base) {
		return base
	}
	for ; ; n++ {
		if name := fmt.Sprintf("%s-%d", stem, n); !exists(name) {
			return name
		}
	}
}
