package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// proc is one process from /proc.
type proc struct {
	pid, ppid int
	argv      []string
}

// procs lists the processes other than this one.
func procs() []proc {
	ents, _ := os.ReadDir("/proc")
	self := os.Getpid()
	var out []proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || len(b) == 0 {
			continue
		}
		p := proc{pid: pid, argv: strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")}
		p.ppid, _ = statField(pid, 1)
		out = append(out, p)
	}
	return out
}

// statField returns field i (0 = state) after the command name of
// /proc/<pid>/stat as an int, or for i = 0 the state letter as a rune.
func statField(pid, i int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	j := strings.LastIndexByte(s, ')')
	if j < 0 {
		return 0, false
	}
	f := strings.Fields(s[j+1:])
	if i >= len(f) {
		return 0, false
	}
	if i == 0 {
		return int(f[0][0]), true
	}
	v, err := strconv.Atoi(f[i])
	return v, err == nil
}

// gone reports whether pid has exited: /proc/<pid> is absent or the process
// is a zombie (it is not our child, so we cannot reap it).
func gone(pid int) bool {
	st, ok := statField(pid, 0)
	return !ok || st == 'Z'
}

// waitGone polls every 100 ms until every pid is gone or d passes, and
// returns the survivors.
func waitGone(pids []int, d time.Duration) []int {
	deadline := time.Now().Add(d)
	for {
		var left []int
		for _, p := range pids {
			if !gone(p) {
				left = append(left, p)
			}
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isProgram reports whether argv[0] names the program at path. Under the
// test environment's emulator argv[0] is the guest path as well.
func isProgram(argv []string, path string) bool {
	return len(argv) > 0 && (argv[0] == path || filepath.Base(argv[0]) == filepath.Base(path))
}

func mentions(argv []string, s string) bool {
	for _, a := range argv {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}

func kill(pids []int, sig syscall.Signal) {
	for _, p := range pids {
		_ = syscall.Kill(p, sig)
	}
}
