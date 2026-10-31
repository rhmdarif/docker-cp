// Package procenv builds the environment for processes the panel spawns
// (host shells, docker compose). The panel's own tuning (GOMEMLIMIT, GOGC)
// and configuration (DCP_*) must not leak into them: a Go tool inheriting a
// 40 MiB memory limit would crawl.
package procenv

import (
	"os"
	"strings"
)

// Filter returns env without GOMEMLIMIT, GOGC and DCP_* entries.
func Filter(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GOMEMLIMIT" || k == "GOGC" || strings.HasPrefix(k, "DCP_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Child returns the filtered os.Environ() followed by extra entries.
func Child(extra ...string) []string {
	return append(Filter(os.Environ()), extra...)
}
