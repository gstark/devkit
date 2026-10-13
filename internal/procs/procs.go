// Package procs finds the server processes that run from inside a checkout
// and stops them, for `devkit complete`.
package procs

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/gstark/devkit/internal/proc"
)

// Match is a process that belongs to the checkout.
type Match struct {
	PID     int32
	Command string
}

// InCheckout lists processes whose working directory is root or below it
// and whose command line matches one of patterns, or whose pid is in one of
// the pid files. devkit's own process tree is left out.
func InCheckout(root string, patterns []string, pidFiles []string) ([]Match, error) {
	regexes := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("process pattern %q: %w", pattern, err)
		}
		regexes = append(regexes, re)
	}
	listed := map[int32]bool{}
	for _, path := range pidFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			listed[int32(pid)] = true
		}
	}
	own := ownTree()

	all, err := process.Processes()
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, p := range all {
		if own[p.Pid] {
			continue
		}
		cmdline, err := p.Cmdline()
		if err != nil || cmdline == "" {
			continue
		}
		matched := listed[p.Pid]
		for _, re := range regexes {
			if matched {
				break
			}
			matched = re.MatchString(cmdline)
		}
		if !matched {
			continue
		}
		cwd, err := p.Cwd()
		if err != nil || !inDir(cwd, root) {
			continue
		}
		matches = append(matches, Match{PID: p.Pid, Command: cmdline})
	}
	return matches, nil
}

// StopAll kills each match and its descendants, waits up to ten seconds, and
// reports the survivors.
func StopAll(matches []Match, out io.Writer) error {
	for _, m := range matches {
		fmt.Fprintf(out, "Stopping %d: %s\n", m.PID, truncate(m.Command, 90))
		killTree(m.PID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if survivors(matches) == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	var left []string
	for _, m := range matches {
		if alive(m.PID) {
			_ = syscall.Kill(int(m.PID), syscall.SIGKILL)
			left = append(left, fmt.Sprintf("  %d %s", m.PID, truncate(m.Command, 90)))
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("these processes did not stop on TERM and were killed:\n%s", strings.Join(left, "\n"))
	}
	return nil
}

func killTree(pid int32) {
	if p, err := process.NewProcess(pid); err == nil {
		if children, err := p.Children(); err == nil {
			for _, child := range children {
				killTree(child.Pid)
			}
		}
	}
	_ = syscall.Kill(int(pid), syscall.SIGTERM)
}

func survivors(matches []Match) int {
	count := 0
	for _, m := range matches {
		if alive(m.PID) {
			count++
		}
	}
	return count
}

func alive(pid int32) bool {
	return proc.Alive(int(pid))
}

// ownTree is devkit and its ancestors: the shells and `just` that started it.
func ownTree() map[int32]bool {
	own := map[int32]bool{}
	pid := int32(os.Getpid())
	for pid > 1 {
		own[pid] = true
		p, err := process.NewProcess(pid)
		if err != nil {
			break
		}
		parent, err := p.Ppid()
		if err != nil {
			break
		}
		pid = parent
	}
	return own
}

func inDir(path, root string) bool {
	root = strings.TrimRight(root, "/")
	return path == root || strings.HasPrefix(path, root+"/")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
