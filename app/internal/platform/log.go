package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labeth/open-sds800xhd/ota/fsguard"
)

// maxLogBytes is where the log file is rotated to <name>.1.
const maxLogBytes = 1 << 20

// Log writes lines to a file on p12 and to stderr. The file is synced after
// every line: the unit has no watchdog and a hang ends in a power cycle, so a
// line not on the flash is lost (spec 01 §10, §12.2). /dev/kmsg is not used:
// the kernel rate-limits it.
type Log struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

// OpenLog opens (appends to) LogDir/<name>.log. If the file cannot be
// opened the log goes to stderr only, and the error is returned with it.
func OpenLog(name string) (*Log, error) {
	l := &Log{path: filepath.Join(LogDir, name+".log")}
	err := l.open()
	return l, err
}

func (l *Log) open() error {
	if err := fsguard.MkdirAll(filepath.Dir(l.path)); err != nil {
		return err
	}
	f, err := fsguard.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Printf logs one line.
func (l *Log) Printf(format string, a ...any) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05.000Z ") + fmt.Sprintf(format, a...) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprint(os.Stderr, line)
	if l.f == nil {
		return
	}
	if l.size+int64(len(line)) > maxLogBytes {
		_ = l.f.Close()
		l.f = nil
		_ = fsguard.Rename(l.path, l.path+".1")
		if l.open() != nil {
			return
		}
	}
	n, _ := l.f.WriteString(line)
	l.size += int64(n)
	_ = l.f.Sync()
}

// Close closes the file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// markNode is the test environment's marker device (spec 16): a line
// written to it lands in the simulator's trace, so trace windows can be cut
// at the app's milestones. It does not exist on a unit.
const markNode = "/dev/simd_mark"

// Mark records a milestone in the test environment's trace; on a unit it does
// nothing.
func Mark(text string) {
	if fsguard.CheckDevice(markNode) != nil {
		return
	}
	f, err := os.OpenFile(markNode, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString("app: " + text + "\n")
	_ = f.Close()
}
