package procenv

import (
	"reflect"
	"testing"
)

func TestFilter(t *testing.T) {
	in := []string{"PATH=/bin", "GOMEMLIMIT=40MiB", "GOGC=50", "DCP_LISTEN=:1", "DCP_=x", "HOME=/root", "GOGCX=1", "MYDCP_X=1"}
	want := []string{"PATH=/bin", "HOME=/root", "GOGCX=1", "MYDCP_X=1"}
	if got := Filter(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestChild(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "40MiB")
	t.Setenv("DCP_DATA_DIR", "/x")
	t.Setenv("KEEP_ME", "1")
	got := Child("TERM=xterm-256color")
	has := map[string]bool{}
	for _, kv := range got {
		has[kv] = true
	}
	if has["GOMEMLIMIT=40MiB"] || has["DCP_DATA_DIR=/x"] || !has["KEEP_ME=1"] || !has["TERM=xterm-256color"] {
		t.Fatalf("env %q", got)
	}
}
