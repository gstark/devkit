// Package proc starts, watches, and stops server processes.
package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// BootTimeout bounds how long a server may take to answer its ready URL.
const BootTimeout = 15 * time.Second

// Started records one server devkit started.
type Started struct {
	Name          string `json:"name"`
	PID           int    `json:"pid"`
	Port          int    `json:"port"`
	Log           string `json:"log"`
	Dir           string `json:"dir,omitempty"`
	Ephemeral     bool   `json:"ephemeral,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
}

// PidFile is the record of one `devkit services start` run.
type PidFile struct {
	Path     string    `json:"-"`
	OwnerPID int       `json:"owner_pid"`
	Root     string    `json:"root"`
	Profile  string    `json:"profile"`
	Started  time.Time `json:"started"`
	Services []Started `json:"services"`
}

// Save writes the pid file.
func (p *PidFile) Save() error {
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.Path, data, 0o644)
}

// LoadPidFile reads a pid file.
func LoadPidFile(path string) (*PidFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p PidFile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	return &p, nil
}

// ListPidFiles returns every pid file in dir, oldest first.
func ListPidFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.pids.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// OwnerAlive reports whether the process that started the servers still runs.
func (p *PidFile) OwnerAlive() bool {
	return Alive(p.OwnerPID)
}

// Alive reports whether a process with the pid exists and is not a zombie.
// kill(pid, 0) succeeds on a zombie, so a child that died but was not yet
// reaped by its parent would otherwise look alive forever.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	statuses, err := p.Status()
	if err != nil {
		return true
	}
	for _, status := range statuses {
		if status == process.Zombie {
			return false
		}
	}
	return true
}

// Spawn starts command in its own session with stdout and stderr appended to
// logPath. The child outlives devkit: a test process or a shell script asks
// devkit to start servers and exits, and the servers stay up until `stop`.
func Spawn(command []string, env map[string]string, dir, logPath string) (*os.Process, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer log.Close()

	path, err := exec.LookPath(command[0])
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, command[1:]...)
	cmd.Dir = dir
	cmd.Env = mergedEnv(env)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := cmd.Process
	// Reap the child if it exits while devkit still runs; after devkit exits
	// init adopts it.
	go func() { _ = cmd.Wait() }()
	return process, nil
}

// Child starts command as a plain child with output to devkit's own stdout
// and stderr, for `services up` under a Procfile runner.
func Child(command []string, env map[string]string, dir string) (*exec.Cmd, error) {
	path, err := exec.LookPath(command[0])
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, command[1:]...)
	cmd.Dir = dir
	cmd.Env = mergedEnv(env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// WaitReady polls url until it answers 2xx. It fails as soon as alive
// reports the server gone, and after BootTimeout.
func WaitReady(ctx context.Context, url string, alive func() bool) error {
	deadline := time.Now().Add(BootTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		if !alive() {
			return errors.New("exited during startup")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("did not answer %s within %s", url, BootTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop ends a server: TERM, a short wait, then KILL. The wait is short on
// purpose; LiveKit drains its rooms on TERM, which can take minutes. A Docker
// service is stopped through its container name instead.
func Stop(s Started) error {
	if s.ContainerName != "" {
		out, err := exec.Command("docker", "stop", "--time", "2", s.ContainerName).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such container") {
			return fmt.Errorf("docker stop %s: %s", s.ContainerName, strings.TrimSpace(string(out)))
		}
	}
	if !Alive(s.PID) {
		return nil
	}
	_ = syscall.Kill(s.PID, syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		if !Alive(s.PID) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(s.PID, syscall.SIGKILL)
	return nil
}

func mergedEnv(extra map[string]string) []string {
	env := os.Environ()
	if len(extra) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(extra))
	for _, pair := range env {
		key, _, _ := strings.Cut(pair, "=")
		if _, replaced := extra[key]; !replaced {
			out = append(out, pair)
		}
	}
	for key, value := range extra {
		out = append(out, key+"="+value)
	}
	return out
}
