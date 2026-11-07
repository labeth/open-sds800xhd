package board

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// proc is one process from /proc.
type proc struct {
	pid, ppid int
	argv      []string
}

// procs lists the processes other than this one.
func procs(root string) []proc {
	ents, _ := os.ReadDir(filepath.Join("/", root, "proc"))
	self := os.Getpid()
	var out []proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/", root, "proc", e.Name(), "cmdline")) //nolint:gosec // G304: a /proc entry under the board's root
		if err != nil || len(b) == 0 {
			continue
		}
		p := proc{pid: pid, argv: strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")}
		if f, ok := stat(root, pid); ok && len(f) > 1 {
			p.ppid, _ = strconv.Atoi(f[1])
		}
		out = append(out, p)
	}
	return out
}

// stat returns the fields of /proc/<pid>/stat after the command name: 0 is
// the state letter, 1 the parent.
func stat(root string, pid int) ([]string, bool) {
	b, err := os.ReadFile(filepath.Join("/", root, "proc", strconv.Itoa(pid), "stat")) //nolint:gosec // G304: a /proc entry under the board's root
	if err != nil {
		return nil, false
	}
	s := string(b)
	j := strings.LastIndexByte(s, ')')
	if j < 0 {
		return nil, false
	}
	return strings.Fields(s[j+1:]), true
}

// alive reports whether pid runs: /proc/<pid> is there and the process is
// not a zombie (it need not be our child, so it cannot be reaped).
func alive(root string, pid int) bool {
	f, ok := stat(root, pid)
	return ok && len(f) > 0 && f[0] != "Z"
}

// waitGone polls every 100 ms until every pid is gone or d passes, and
// returns the survivors.
func waitGone(root string, pids []int, d time.Duration) []int {
	deadline := time.Now().Add(d)
	for {
		var left []int
		for _, p := range pids {
			if alive(root, p) {
				left = append(left, p)
			}
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isProgram reports whether argv[0] names the program at path; under the
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
