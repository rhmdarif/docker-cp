package caddy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"docker-cp/internal/store"
)

func TestValidateDomains(t *testing.T) {
	got, err := ValidateDomains([]string{"A.Klien.com."}, "", nil, "")
	if err != nil || !reflect.DeepEqual(got, []string{"a.klien.com"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := ValidateDomains([]string{"10.1.2.3"}, "", nil, ""); err != nil || got[0] != "10.1.2.3" {
		t.Fatalf("ipv4: %v %v", got, err)
	}
	bad := [][]string{{"a b.com"}, {"a{.com"}, {"*.x.com"}, {""}, {}, nil, {"a.com", "A.com"}, {"nodot"}, {"a.com\n}"}}
	for _, b := range bad {
		if _, err := ValidateDomains(b, "", nil, ""); err == nil {
			t.Errorf("expected error for %q", b)
		}
	}
}

func TestValidateDomainsDuplicate(t *testing.T) {
	all := []store.Site{{ID: "1", Domains: []string{"a.com"}}, {ID: "2", Domains: []string{"b.com"}}}
	_, err := ValidateDomains([]string{"a.com"}, "2", all, "")
	if err == nil || !strings.Contains(err.Error(), "domain sudah dipakai site lain") {
		t.Fatalf("err %v", err)
	}
	if _, err := ValidateDomains([]string{"b.com"}, "2", all, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDomains([]string{"Panel.com"}, "2", all, "panel.com"); err == nil {
		t.Fatal("panel domain should be rejected")
	}
}

func TestValidateCustomBraces(t *testing.T) {
	ok := []string{
		"header {\n\tX 1\n}",
		`respond "}"`,
		"respond `}`",
		"# } comment\nencode gzip",
		`respond "a\"}"`,
		"",
	}
	for _, s := range ok {
		if err := ValidateCustom(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := []string{
		"}\nevil.com {",
		"header {",
		"}",
		`respond "unterminated }`,
		// a quote in mid-token is literal for Caddy, so the } is real
		"x\" \n}\nevil.com {\ny\"",
		"respond <<EOF\n{\nEOF",
		"a {\n} {",
	}
	for _, s := range bad {
		if err := ValidateCustom(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
	if err := ValidateCustom("}\nevil.com {"); err == nil || !strings.Contains(err.Error(), "kurung kurawal tidak seimbang") {
		t.Fatalf("err %v", err)
	}
}

func TestValidateManualText(t *testing.T) {
	ok := []string{
		"a.com {\n respond hi\n}",
		"a.com, b.com {\n header {\n  X 1\n }\n}\n",
		"a.com {\n respond \"}\"\n}",
	}
	for _, s := range ok {
		if err := ValidateManualText(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := []string{
		"{\n debug\n}\na.com {}",
		"{\n debug\n}",
		"a.com {\n}\nb.com {\n}",
		"(snip) {\n}",
		"import x",
		"a.com {\n import x\n}",
		"a.com {\n}\nrespond x",
		"a.com {\n respond hi",
		"a.com\nrespond hi",
		"",
		"a.com{\n}",
	}
	for _, s := range bad {
		if err := ValidateManualText(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func adaptedWith(routes string) json.RawMessage {
	return json.RawMessage(`{"apps":{"http":{"servers":{"srv0":{"routes":` + routes + `}}}}}`)
}

func TestValidateManualAdapted(t *testing.T) {
	cases := []struct {
		name    string
		routes  string
		other   []string
		wantErr string
	}{
		{"no match", `[{"handle":[]}]`, nil, "catch-all"},
		{"empty host", `[{"match":[{"host":[]}],"handle":[]}]`, nil, "catch-all"},
		{"path only", `[{"match":[{"path":["/x"]}]}]`, nil, "catch-all"},
		{"one entry without host", `[{"match":[{"host":["a.com"]},{"path":["/x"]}]}]`, nil, "catch-all"},
		{"wildcard hits other", `[{"match":[{"host":["*.klien.com"]}]}]`, []string{"a.klien.com"}, "domain"},
		{"wildcard misses deeper", `[{"match":[{"host":["*.klien.com"]}]}]`, []string{"a.b.klien.com"}, ""},
		{"same as other", `[{"match":[{"host":["A.com"]}]}]`, []string{"a.com"}, "domain"},
		{"fine", `[{"match":[{"host":["b.com"]}]}]`, []string{"a.com"}, ""},
	}
	for _, c := range cases {
		err := ValidateManualAdapted(adaptedWith(c.routes), c.other)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err %v want %q", c.name, err, c.wantErr)
		}
	}
	if err := ValidateManualAdapted(json.RawMessage(`not json`), nil); err == nil {
		t.Error("invalid json should fail")
	}
}

func TestValidateCustomCaddyTokenization(t *testing.T) {
	bad := []string{
		"respond a{\n}\nevil.com {\n\treverse_proxy localhost:2019\n}\nx.com {\n\trespond b}",
		"respond \\\"\n\"\n}\nevil.com {\n\treverse_proxy localhost:2019\n}\nx.com {\n\trespond x\"",
		"import /etc/caddy/other",
		"header {\n\timport snip\n}",
	}
	for _, s := range bad {
		if err := ValidateCustom(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
	for _, s := range []string{"respond {remote_host}", `respond "}"`, "header X-Ip {http.request.remote_host}"} {
		if err := ValidateCustom(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestValidateManualTextInjection(t *testing.T) {
	s := "a.com {\n\trespond a{\n}\n:80 {\n\trespond hijack\n}\nx.com {\n\trespond b}\n}"
	if err := ValidateManualText(s); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateManualText("a.com {\n\trespond {remote_host}\n}"); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptedWildcardLabels(t *testing.T) {
	for _, h := range []string{"*.*.com", "a.*.com", "*.klien.com"} {
		err := ValidateManualAdapted(adaptedWith(`[{"match":[{"host":["`+h+`"]}]}]`), []string{"a.klien.com"})
		if err == nil {
			t.Errorf("%s should collide", h)
		}
	}
	err := ValidateManualAdapted(adaptedWith(`[{"match":[{"host":["*.com"]}]}]`), []string{"a.klien.com"})
	if err != nil {
		t.Errorf("*.com must not match a.klien.com: %v", err)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, e := range []string{"", "a@b.c"} {
		if err := ValidateEmail(e); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
	for _, e := range []string{"a b@c.d", "a@b@c", "ab", "a@b.c\n}", "a{@b.c", "\"a\"@b.c", "a#@b.c"} {
		if ValidateEmail(e) == nil {
			t.Errorf("%q should fail", e)
		}
	}
}

func TestRejectCRAndEnvSubstitution(t *testing.T) {
	custom := []string{
		"respond a\r{\n}\nevil.com {\n\treverse_proxy localhost:2019\n}\nx.com {\n\trespond b\r}",
		"respond {$X:#} {\n}\nevil.com {\n\treverse_proxy localhost:2019\n}\nx.com {\n\trespond b {$X:#} }",
		"respond \"{$X:\"",
		"respond {$SECRET}",
	}
	for _, s := range custom {
		if ValidateCustom(s) == nil {
			t.Errorf("custom %q accepted", s)
		}
	}
	manual := []string{
		"a.com {\n\trespond a\r{\n}\n:80 {\n\trespond hijack\n}\nx.com {\n\trespond b\r}\n}",
		"a.com {\n\trespond {$X:#} {\n}\n:80 {\n\trespond hijack\n}\nx.com {\n\trespond b {$X:#} }\n}",
	}
	for _, s := range manual {
		if ValidateManualText(s) == nil {
			t.Errorf("manual %q accepted", s)
		}
	}
}

func TestNormalizeNewlines(t *testing.T) {
	if got := NormalizeNewlines("a\r\nb\rc"); got != "a\nb\nc" {
		t.Fatalf("%q", got)
	}
	in := "header {\r\n\tX 1\r\n}"
	if ValidateCustom(in) == nil {
		t.Fatal("raw CRLF should be rejected")
	}
	if err := ValidateCustom(NormalizeNewlines(in)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManualText(NormalizeNewlines("a.com {\r\n respond hi\r\n}")); err != nil {
		t.Fatal(err)
	}
}

func TestWildcardSymmetric(t *testing.T) {
	err := ValidateManualAdapted(adaptedWith(`[{"match":[{"host":["a.klien.com"]}]}]`), []string{"*.klien.com"})
	if err == nil {
		t.Error("concrete host must collide with other wildcard")
	}
	all := []store.Site{{ID: "1", Domains: []string{"*.klien.com"}}}
	if _, err := ValidateDomains([]string{"a.klien.com"}, "", all, ""); err == nil {
		t.Error("ValidateDomains must collide with existing wildcard")
	}
	if _, err := ValidateDomains([]string{"a.klien.com"}, "", nil, "*.klien.com"); err == nil {
		t.Error("ValidateDomains must collide with wildcard panel domain")
	}
}
