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
