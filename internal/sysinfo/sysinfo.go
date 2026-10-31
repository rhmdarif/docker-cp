// Package sysinfo reads host CPU, memory, disk and uptime from /proc.
package sysinfo

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Info struct {
	CPUPercent          float64
	MemTotal, MemUsed   uint64
	DiskTotal, DiskUsed uint64
	Uptime              time.Duration
	Load1               float64
}

type CPUTimes struct{ Idle, Total uint64 }

// ParseCPU parses the aggregate "cpu  ..." line of /proc/stat.
// Idle is idle+iowait; Total is the sum of all fields.
func ParseCPU(statLine string) (CPUTimes, error) {
	f := strings.Fields(statLine)
	if len(f) < 6 || f[0] != "cpu" {
		return CPUTimes{}, fmt.Errorf("sysinfo: invalid cpu line %q", statLine)
	}
	var t CPUTimes
	for i, s := range f[1:] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return CPUTimes{}, fmt.Errorf("sysinfo: invalid cpu field %q: %w", s, err)
		}
		t.Total += v
		if i == 3 || i == 4 {
			t.Idle += v
		}
	}
	return t, nil
}

// CPUPercent returns the busy percentage between two samples.
func CPUPercent(a, b CPUTimes) float64 {
	if b.Total <= a.Total || b.Idle < a.Idle {
		return 0
	}
	total := float64(b.Total - a.Total)
	idle := float64(b.Idle - a.Idle)
	if idle > total {
		return 0
	}
	return (total - idle) / total * 100
}

// ParseMeminfo returns MemTotal and MemAvailable in bytes.
func ParseMeminfo(r io.Reader) (total, available uint64, err error) {
	var gotT, gotA bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || (f[0] != "MemTotal:" && f[0] != "MemAvailable:") {
			continue
		}
		v, perr := strconv.ParseUint(f[1], 10, 64)
		if perr != nil {
			return 0, 0, fmt.Errorf("sysinfo: invalid meminfo value %q: %w", f[1], perr)
		}
		if len(f) > 2 && strings.EqualFold(f[2], "kB") {
			v *= 1024
		}
		if f[0] == "MemTotal:" {
			total, gotT = v, true
		} else {
			available, gotA = v, true
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotT || !gotA {
		return 0, 0, fmt.Errorf("sysinfo: MemTotal/MemAvailable not found")
	}
	return total, available, nil
}

func readCPU(procRoot string) (CPUTimes, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return CPUTimes{}, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return ParseCPU(line)
}

func firstField(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("sysinfo: %s is empty", path)
	}
	return strconv.ParseFloat(f[0], 64)
}

// Read samples /proc/stat twice, sample apart, and gathers memory, disk,
// uptime and load. It starts no goroutines.
func Read(procRoot, diskPath string, sample time.Duration) (Info, error) {
	var info Info
	a, err := readCPU(procRoot)
	if err != nil {
		return info, err
	}
	time.Sleep(sample)
	b, err := readCPU(procRoot)
	if err != nil {
		return info, err
	}
	info.CPUPercent = CPUPercent(a, b)

	mf, err := os.Open(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return info, err
	}
	defer mf.Close()
	total, avail, err := ParseMeminfo(mf)
	if err != nil {
		return info, err
	}
	info.MemTotal = total
	if total > avail {
		info.MemUsed = total - avail
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &st); err != nil {
		return info, err
	}
	bs := uint64(st.Bsize)
	info.DiskTotal = st.Blocks * bs
	info.DiskUsed = (st.Blocks - st.Bfree) * bs

	up, err := firstField(filepath.Join(procRoot, "uptime"))
	if err != nil {
		return info, err
	}
	info.Uptime = time.Duration(up * float64(time.Second))
	if info.Load1, err = firstField(filepath.Join(procRoot, "loadavg")); err != nil {
		return info, err
	}
	return info, nil
}
