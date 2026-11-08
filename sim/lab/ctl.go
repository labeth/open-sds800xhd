package lab

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"syscall"
	"time"
)

// errNoSim is returned while no simulator run is reachable.
var errNoSim = errors.New("no simulator running")

// errModelGone is a control socket that no longer answers: the run's
// device model has stopped.
var errModelGone = errors.New("the device model stopped")

// ctl sends one command to the device model's control socket at path and
// returns its reply line. A command opens its own connection: the model
// serves each connection in order, and a key waits there until the
// firmware reads the key device, which must not hold up a screen dump.
func ctl(ctx context.Context, path, cmd string) (string, error) {
	if path == "" {
		return "", errNoSim
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%w: %w", errModelGone, err)
		}
		return "", fmt.Errorf("control socket: %w", err)
	}
	defer func() { _ = c.Close() }() // a reply already read is all the command returns
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintln(c, cmd); err != nil {
		return "", err
	}
	rep, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%s: %w", cmd, err)
	}
	rep = strings.TrimSpace(rep)
	if after, ok := strings.CutPrefix(rep, "err"); ok {
		return rep, fmt.Errorf("%s", strings.TrimSpace(after))
	}
	return rep, nil
}
