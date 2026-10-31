package caddy

import (
	"errors"
	"os"
	"strings"
	"testing"

	"docker-cp/internal/store"
)

func fakeResolve(t store.Target) (string, error) {
	if t.Container != "" {
		if t.Container == "gone" {
			return "", errors.New("container gone tidak ditemukan")
		}
		return "127.0.0.1:8001", nil
	}
	return t.Host, nil
}

func TestGenerateBasic(t *testing.T) {
	st := store.State{
		Settings: store.Settings{PanelDomain: "panel.example.com", PanelPort: 8800, ACMEEmail: "a@b.c"},
		Sites: []store.Site{
			{ID: "1", Domains: []string{"a.klien.com", "www.a.klien.com"}, Target: store.Target{Container: "web", ContainerPort: 80}},
			{ID: "2", Domains: []string{"b.klien.com"}, Target: store.Target{Host: "10.0.0.5:3000"}, Custom: "encode gzip\nheader X-A 1"},
		},
	}
	got, warns := Generate(st, fakeResolve)
	want, err := os.ReadFile("testdata/basic.caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
}

func TestGenerateNoEmailNoPanel(t *testing.T) {
	got, _ := Generate(store.State{Settings: store.Settings{PanelPort: 8800}}, fakeResolve)
	if got != "{\n\tadmin localhost:2019\n}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestGenerateManual(t *testing.T) {
	blk := "m.com {\n\trespond hi\n}"
	st := store.State{
		Settings: store.Settings{PanelPort: 8800},
		Sites: []store.Site{
			{ID: "m", Manual: true, ManualBlock: "\n" + blk + "\n\n", Target: store.Target{Container: "gone"}},
		},
	}
	got, warns := Generate(st, func(store.Target) (string, error) { t.Fatal("resolve called for manual"); return "", nil })
	if !strings.HasSuffix(got, "\n\n"+blk+"\n") {
		t.Fatalf("got %q", got)
	}
	if len(warns) != 0 {
		t.Fatal(warns)
	}
}

func TestGenerateMissingContainer(t *testing.T) {
	st := store.State{
		Settings: store.Settings{PanelPort: 8800},
		Sites: []store.Site{
			{ID: "a", Domains: []string{"a.com"}, Target: store.Target{Container: "gone"}, Custom: "encode gzip"},
			{ID: "b", Domains: []string{"b.com"}, Target: store.Target{Container: "web"}},
		},
	}
	got, warns := Generate(st, fakeResolve)
	if !strings.Contains(got, "a.com {\n\trespond \"docker-cp: target tidak tersedia\" 502\n\tencode gzip\n}") {
		t.Fatalf("block A wrong:\n%s", got)
	}
	if !strings.Contains(got, "b.com {\n\treverse_proxy 127.0.0.1:8001\n}") {
		t.Fatalf("block B wrong:\n%s", got)
	}
	if len(warns) != 1 || !strings.HasPrefix(warns[0], "a.com: ") {
		t.Fatalf("warnings: %v", warns)
	}
}

func TestGenerateSkipsSiteWithoutDomains(t *testing.T) {
	st := store.State{Sites: []store.Site{{ID: "x", Target: store.Target{Host: "1.2.3.4:80"}}}}
	got, warns := Generate(st, fakeResolve)
	if strings.Contains(got, "reverse_proxy") || len(warns) != 1 {
		t.Fatalf("got %q warns %v", got, warns)
	}
}

func TestGenerateDefensive(t *testing.T) {
	st := store.State{
		Settings: store.Settings{PanelDomain: "p.com {", PanelPort: 8800, ACMEEmail: "a@b.c\n}\nevil.com {"},
		Sites: []store.Site{
			{ID: "1", Domains: []string{"a.com\n}\nevil.com {"}, Target: store.Target{Host: "1.2.3.4:80"}},
			{ID: "2", Domains: []string{"ok.com"}, Target: store.Target{Host: "1.2.3.4:80"}},
		},
	}
	got, warns := Generate(st, fakeResolve)
	if strings.Contains(got, "evil") || strings.Contains(got, "p.com") || strings.Contains(got, "email") {
		t.Fatalf("unsafe output:\n%s", got)
	}
	if !strings.Contains(got, "ok.com {") || len(warns) != 3 {
		t.Fatalf("got %s warns %v", got, warns)
	}
}

func TestGenerateRevalidatesStoredInput(t *testing.T) {
	st := store.State{
		Settings: store.Settings{PanelPort: 8800},
		Sites: []store.Site{
			{ID: "1", Domains: []string{"a.com"}, Target: store.Target{Host: "1.2.3.4:80"}, Custom: "}\nevil.com {"},
			{ID: "2", Manual: true, ManualBlock: "{\n debug\n}\nx.com {\n}"},
			{ID: "3", Domains: []string{"ok.com"}, Target: store.Target{Host: "1.2.3.4:80"}},
		},
	}
	got, warns := Generate(st, fakeResolve)
	if strings.Contains(got, "evil") || strings.Contains(got, "debug") || strings.Contains(got, "a.com") {
		t.Fatalf("bad stored data emitted:\n%s", got)
	}
	if !strings.Contains(got, "ok.com {") || len(warns) != 2 {
		t.Fatalf("got %s warns %v", got, warns)
	}
}
