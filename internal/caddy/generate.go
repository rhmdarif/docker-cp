// Package caddy renders the Caddyfile from stored sites and validates every
// piece of user-supplied input that ends up inside it.
package caddy

import (
	"fmt"
	"strings"

	"docker-cp/internal/store"
)

// Resolver maps a site target to an upstream such as "127.0.0.1:8001".
type Resolver func(t store.Target) (upstream string, err error)

const unavailableLine = `respond "docker-cp: target tidak tersedia" 502`

// Generate renders the full Caddyfile. Sites whose target cannot be resolved
// still get a block (answering 502) so one missing container never affects
// the others; each such site adds a warning.
func Generate(st store.State, resolve Resolver) (caddyfile string, warnings []string) {
	var blocks []string

	global := "{\n\tadmin localhost:2019\n"
	if err := ValidateEmail(st.Settings.ACMEEmail); err != nil {
		warnings = append(warnings, fmt.Sprintf("email ACME diabaikan: %v", err))
	} else if st.Settings.ACMEEmail != "" {
		global += "\temail " + st.Settings.ACMEEmail + "\n"
	}
	blocks = append(blocks, global+"}")

	if pd := st.Settings.PanelDomain; pd != "" && unsafeDomain(pd) {
		warnings = append(warnings, fmt.Sprintf("domain panel diabaikan: tidak valid: %q", pd))
	} else if pd != "" {
		blocks = append(blocks, fmt.Sprintf("%s {\n\treverse_proxy 127.0.0.1:%d\n}", st.Settings.PanelDomain, st.Settings.PanelPort))
	}

	for _, s := range st.Sites {
		if s.Manual {
			b := strings.TrimSpace(s.ManualBlock)
			if err := ValidateManualText(b); b != "" && err != nil {
				warnings = append(warnings, fmt.Sprintf("site %s dilewati: blok manual tidak valid: %v", s.ID, err))
			} else if b != "" {
				blocks = append(blocks, b)
			}
			continue
		}
		if len(s.Domains) == 0 {
			warnings = append(warnings, fmt.Sprintf("site %s: tidak ada domain", s.ID))
			continue
		}
		if bad := firstUnsafe(s.Domains); bad != "" {
			warnings = append(warnings, fmt.Sprintf("site %s dilewati: domain tidak valid: %q", s.ID, bad))
			continue
		}
		if err := ValidateCustom(s.Custom); err != nil {
			warnings = append(warnings, fmt.Sprintf("site %s dilewati: custom tidak valid: %v", s.ID, err))
			continue
		}
		if s.Target.Container == "" {
			if err := ValidateUpstream(s.Target.Host); err != nil {
				warnings = append(warnings, fmt.Sprintf("site %s dilewati: target tidak valid: %v", s.ID, err))
				continue
			}
		}
		body := unavailableLine
		upstream, err := resolve(s.Target)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", s.Domains[0], err))
		} else if err := ValidateUpstream(upstream); err != nil {
			warnings = append(warnings, fmt.Sprintf("site %s dilewati: target tidak valid: %v", s.ID, err))
			continue
		} else {
			body = "reverse_proxy " + upstream
		}
		var sb strings.Builder
		sb.WriteString(strings.Join(s.Domains, ", "))
		sb.WriteString(" {\n\t" + body + "\n")
		if custom := strings.TrimSpace(s.Custom); custom != "" {
			for _, line := range strings.Split(custom, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					sb.WriteString("\n")
					continue
				}
				sb.WriteString("\t" + line + "\n")
			}
		}
		sb.WriteString("}")
		blocks = append(blocks, sb.String())
	}
	return strings.Join(blocks, "\n\n") + "\n", warnings
}

func unsafeDomain(d string) bool { return unsafeToken(d) || strings.Contains(d, ",") }

func firstUnsafe(ds []string) string {
	for _, d := range ds {
		if unsafeDomain(d) {
			return d
		}
	}
	return ""
}
