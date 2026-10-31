package caddy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"docker-cp/internal/store"
)

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$`)

func validDomain(d string) bool {
	if domainRe.MatchString(d) {
		return true
	}
	a, err := netip.ParseAddr(d)
	return err == nil && a.Is4()
}

// ValidateDomains normalizes (lowercase, trailing dot removed) and checks the
// domains of a site. siteID is the site being edited, so its own domains do
// not count as conflicts.
func ValidateDomains(domains []string, siteID string, all []store.Site, panelDomain string) ([]string, error) {
	if len(domains) == 0 {
		return nil, errors.New("minimal satu domain diperlukan")
	}
	used := map[string]bool{}
	for _, s := range all {
		if s.ID == siteID {
			continue
		}
		for _, d := range s.Domains {
			used[strings.ToLower(d)] = true
		}
	}
	panel := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(panelDomain), "."))
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, raw := range domains {
		d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if strings.Contains(d, "*") {
			return nil, fmt.Errorf("domain wildcard tidak diizinkan: %q", raw)
		}
		if !validDomain(d) {
			return nil, fmt.Errorf("domain tidak valid: %q", raw)
		}
		if seen[d] {
			return nil, fmt.Errorf("domain duplikat: %s", d)
		}
		seen[d] = true
		for u := range used {
			if hostsOverlap(d, u) {
				return nil, fmt.Errorf("domain sudah dipakai site lain: %s", d)
			}
		}
		if panel != "" && hostsOverlap(d, panel) {
			return nil, fmt.Errorf("domain sudah dipakai panel: %s", d)
		}
		out = append(out, d)
	}
	return out, nil
}

var upstreamHostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

// ValidateUpstream checks a reverse_proxy upstream, which is written verbatim
// into the Caddyfile. Accepted: IPv4:port, [IPv6 without zone]:port and
// hostname:port (letters, digits, "-", "."), port 1-65535.
func ValidateUpstream(hostport string) error {
	bad := fmt.Errorf("upstream harus berbentuk host:port, mis. 127.0.0.1:9000: %q", hostport)
	if unsafeToken(hostport) || strings.ContainsFunc(hostport, unicode.IsControl) || strings.Contains(hostport, "{$") {
		return bad
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port == "" || strings.Trim(port, "0123456789") != "" {
		return bad
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return bad
	}
	if strings.HasPrefix(hostport, "[") {
		if a, err := netip.ParseAddr(host); err != nil || !a.Is6() || a.Zone() != "" {
			return bad
		}
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Is4() {
			return bad
		}
		return nil
	}
	if !upstreamHostRe.MatchString(host) {
		return bad
	}
	return nil
}

// ValidateCustom checks that custom directives cannot leave their site block.
func ValidateCustom(custom string) error {
	toks, err := scan(custom)
	if err != nil {
		return err
	}
	if hasImport(toks) {
		return errImport
	}
	return nil
}

var errImport = errors.New("import tidak diizinkan")

// ValidateEmail accepts an empty address or a single plain token with exactly
// one "@".
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if strings.Count(email, "@") != 1 || unsafeToken(email) || strings.Contains(email, ",") {
		return errors.New("email tidak valid")
	}
	if strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return errors.New("email tidak valid")
	}
	return nil
}

// unsafeToken reports whether s contains anything that could change how
// Caddy tokenizes the line it is written into.
func unsafeToken(s string) bool {
	return s == "" || strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("{}\"`#\\", r)
	})
}

// ValidateManualText checks the raw text of a manual site block: exactly one
// top-level block with an address, no global options, snippets or imports.
func ValidateManualText(block string) error {
	toks, err := scan(block)
	if err != nil {
		return err
	}
	if len(toks) == 0 {
		return errors.New("blok kosong")
	}
	blocks, addrs := 0, 0
	for _, t := range toks {
		if t.text == "import" {
			return errImport
		}
		if t.depth == 0 {
			if t.endDepth > 0 {
				if t.text != "{" {
					return errors.New("kurung kurawal harus dipisah spasi dari alamat")
				}
				if addrs == 0 {
					return errors.New("opsi global tidak diizinkan: blok harus punya alamat")
				}
				blocks++
				continue
			}
			if blocks > 0 {
				return errors.New("hanya satu blok yang diizinkan")
			}
			if !t.quoted && strings.HasPrefix(t.text, "(") && strings.HasSuffix(t.text, ")") {
				return errors.New("snippet tidak diizinkan")
			}
			addrs++
		}
	}
	if blocks != 1 {
		return errors.New("harus ada tepat satu blok berkurung kurawal")
	}
	return nil
}

// ValidateManualAdapted checks the "result" of Caddy's /adapt for a manual
// block: every route must be host-scoped, and no host may collide with
// otherDomains (exact match, or a wildcard covering one of them).
func ValidateManualAdapted(adapted json.RawMessage, otherDomains []string) error {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
						} `json:"match"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(adapted, &cfg); err != nil {
		return fmt.Errorf("hasil adapt tidak valid: %w", err)
	}
	others := map[string]bool{}
	for _, d := range otherDomains {
		others[strings.ToLower(d)] = true
	}
	names := make([]string, 0, len(cfg.Apps.HTTP.Servers))
	for n := range cfg.Apps.HTTP.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, r := range cfg.Apps.HTTP.Servers[n].Routes {
			if len(r.Match) == 0 {
				return errors.New("alamat catch-all tidak diizinkan")
			}
			// Match entries are OR-ed, so every entry must be host-scoped.
			for _, m := range r.Match {
				if len(m.Host) == 0 {
					return errors.New("alamat catch-all tidak diizinkan")
				}
				for _, h := range m.Host {
					h = strings.ToLower(strings.TrimSpace(h))
					if h == "" || h == "*" || strings.Contains(h, "{") {
						return errors.New("alamat catch-all tidak diizinkan")
					}
					if err := checkHost(h, others); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func checkHost(h string, others map[string]bool) error {
	for o := range others {
		if hostsOverlap(h, o) {
			return fmt.Errorf("domain sudah dipakai site lain: %s", o)
		}
	}
	return nil
}

// hostsOverlap matches label by label; a "*" label on either side matches any
// single label.
func hostsOverlap(a, b string) bool {
	al, bl := strings.Split(a, "."), strings.Split(b, ".")
	if len(al) != len(bl) {
		return false
	}
	for i := range al {
		if al[i] != bl[i] && al[i] != "*" && bl[i] != "*" {
			return false
		}
	}
	return true
}
