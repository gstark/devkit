// Package lock serializes devkit runs across worktrees with a symlink lock.
// ln -s creates the symlink atomically on macOS and Linux, and the link
// target records the holder's pid, so a lock whose process is gone can be
// cleared.
package lock

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Lock is a held lock. Release it when done.
type Lock struct {
	path string
	pid  string
}

// Acquire waits for the lock at path and holds it. It prints one waiting
// message to out, naming what holds it.
func Acquire(path, what string, out io.Writer) (*Lock, error) {
	pid := strconv.Itoa(os.Getpid())
	waiting := false
	for {
		err := os.Symlink(pid, path)
		if err == nil {
			return &Lock{path: path, pid: pid}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		holder, _ := os.Readlink(path)
		if holder != "" && !alive(holder) {
			os.Remove(path)
			continue
		}
		if !waiting {
			fmt.Fprintf(out, "Waiting for %s (pid %s)...\n", what, holder)
			waiting = true
		}
		time.Sleep(time.Second)
	}
}

// Release removes the lock when this process still holds it.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	if holder, _ := os.Readlink(l.path); holder == l.pid {
		os.Remove(l.path)
	}
}

func alive(pid string) bool {
	id, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	return syscall.Kill(id, 0) == nil || errors.Is(syscall.Kill(id, 0), syscall.EPERM)
}
