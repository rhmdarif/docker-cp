package store

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func statePath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "state.json")
}

func TestOpenMissingDefaults(t *testing.T) {
	s, err := Open(statePath(t))
	if err != nil {
		t.Fatal(err)
	}
	st := s.Get()
	if st.Settings.PanelPort != 8800 {
		t.Fatalf("PanelPort = %d", st.Settings.PanelPort)
	}
	if len(st.Sites) != 0 {
		t.Fatalf("sites = %d", len(st.Sites))
	}
}

func TestUpdatePersists(t *testing.T) {
	p := statePath(t)
	s, _ := Open(p)
	err := s.Update(func(st *State) error {
		st.Sites = append(st.Sites, Site{ID: NewID(), Domains: []string{"a.example.com", "b.example.com"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get().Sites
	if len(got) != 1 || !reflect.DeepEqual(got[0].Domains, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateErrorDoesNotPersist(t *testing.T) {
	p := statePath(t)
	s, _ := Open(p)
	_ = s.Update(func(st *State) error {
		st.Sites = append(st.Sites, Site{ID: "one"})
		return nil
	})
	before, _ := os.ReadFile(p)
	err := s.Update(func(st *State) error {
		st.Sites = append(st.Sites, Site{ID: "two"})
		st.Admin.Username = "x"
		return os.ErrInvalid
	})
	if err != os.ErrInvalid {
		t.Fatalf("err = %v", err)
	}
	st := s.Get()
	if len(st.Sites) != 1 || st.Admin.Username != "" {
		t.Fatalf("memory changed: %+v", st)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed")
	}
}

func TestAtomicWrite(t *testing.T) {
	p := statePath(t)
	s, _ := Open(p)
	if err := s.Update(func(st *State) error { st.Admin.Username = "admin"; return nil }); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 || ents[0].Name() != "state.json" {
		t.Fatalf("dir entries: %v", ents)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s, _ := Open(statePath(t))
	_ = s.Update(func(st *State) error {
		st.Sites = append(st.Sites, Site{ID: "x", Domains: []string{"orig.com"}})
		return nil
	})
	s.Get().Sites[0].Domains[0] = "mutated.com"
	if d := s.Get().Sites[0].Domains[0]; d != "orig.com" {
		t.Fatalf("domain = %s", d)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	p := statePath(t)
	s, _ := Open(p)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Update(func(st *State) error {
				st.Sites = append(st.Sites, Site{ID: NewID()})
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s2.Get().Sites); n != 100 {
		t.Fatalf("sites = %d", n)
	}
}
