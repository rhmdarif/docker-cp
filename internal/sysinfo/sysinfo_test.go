package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseCPU(t *testing.T) {
	c, err := ParseCPU("cpu  100 0 100 700 100 0 0 0 0 0")
	if err != nil {
		t.Fatal(err)
	}
	if c.Idle != 800 || c.Total != 1000 {
		t.Fatalf("got %+v", c)
	}
}

func TestParseCPUInvalid(t *testing.T) {
	for _, l := range []string{"", "mem 1 2 3 4 5", "cpu  1 2 3", "cpu  a b c d e"} {
		if _, err := ParseCPU(l); err == nil {
			t.Errorf("expected error for %q", l)
		}
	}
}

func TestCPUPercent(t *testing.T) {
	if got := CPUPercent(CPUTimes{800, 1000}, CPUTimes{850, 1100}); got != 50 {
		t.Fatalf("got %v", got)
	}
	if got := CPUPercent(CPUTimes{1, 1}, CPUTimes{1, 1}); got != 0 {
		t.Fatalf("zero delta got %v", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	f, err := os.Open("testdata/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total, avail, err := ParseMeminfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1024000 || avail != 409600 {
		t.Fatalf("got %d %d", total, avail)
	}
	if _, _, err := ParseMeminfo(strings.NewReader("MemTotal: 5 kB\n")); err == nil {
		t.Fatal("expected error when MemAvailable missing")
	}
}

func TestReadFixture(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"meminfo", "uptime", "loadavg", "stat"} {
		b, err := os.ReadFile(filepath.Join("testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info, err := Read(root, "/", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if info.MemTotal != 1024000 || info.MemUsed != 614400 {
		t.Fatalf("mem %+v", info)
	}
	if info.Uptime != 3600*time.Second+500*time.Millisecond || info.Load1 != 0.52 {
		t.Fatalf("uptime/load %+v", info)
	}
}

func TestReadLocal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	info, err := Read("/proc", "/", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if info.MemTotal == 0 || info.DiskTotal == 0 {
		t.Fatalf("got %+v", info)
	}
	if info.CPUPercent < 0 || info.CPUPercent > 100 {
		t.Fatalf("cpu %v", info.CPUPercent)
	}
}
