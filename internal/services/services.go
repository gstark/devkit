// Package services starts and stops the servers a manifest declares.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gstark/devkit/internal/proc"
	"github.com/gstark/devkit/internal/resolve"
)

// Start runs every enabled service detached and records them in a pid file
// at pidPath. The pid file's owner is the process the servers belong to:
// owner when given, else devkit's parent. A shell script must pass its own
// $$, because `$(devkit services start ...)` runs devkit in a subshell that
// exits at once. On any failure the servers already started are
// stopped again.
func Start(ctx context.Context, plan *resolve.Plan, root, pidPath string, owner int, out io.Writer) (*proc.PidFile, error) {
	if owner <= 0 {
		owner = os.Getppid()
	}
	pidFile := &proc.PidFile{Path: pidPath, OwnerPID: owner, Root: root, Profile: plan.Profile, Started: time.Now()}
	for _, service := range plan.Services {
		if !service.Enabled {
			fmt.Fprintf(out, "%s: not starting, %s.\n", service.Name, service.SkipReason)
			continue
		}
		if err := requireBinary(service); err != nil {
			stopAll(pidFile, out)
			return nil, err
		}
		if err := prepare(service); err != nil {
			stopAll(pidFile, out)
			return nil, fmt.Errorf("%s: %w", service.Name, err)
		}
		logPath := filepath.Join(filepath.Dir(pidPath), fmt.Sprintf("%s-%s.log", service.Name, plan.Profile))
		process, err := proc.Spawn(service.Command, service.ProcEnv, root, logPath)
		if err != nil {
			stopAll(pidFile, out)
			return nil, fmt.Errorf("%s: %w", service.Name, err)
		}
		started := proc.Started{
			Name: service.Name, PID: process.Pid, Port: service.Port, Log: logPath,
			Dir: service.DataDir, Ephemeral: service.Ephemeral, ContainerName: service.ContainerName,
		}
		pidFile.Services = append(pidFile.Services, started)
		if err := proc.WaitReady(ctx, service.ReadyURL, func() bool { return proc.Alive(process.Pid) }); err != nil {
			stopAll(pidFile, out)
			return nil, fmt.Errorf("%s %s. See %s", service.Name, err, logPath)
		}
		if err := postStart(ctx, service); err != nil {
			stopAll(pidFile, out)
			return nil, fmt.Errorf("%s: %w", service.Name, err)
		}
		fmt.Fprintf(out, "%s: ready on port %d, log in %s\n", service.Name, service.Port, logPath)
	}
	if err := pidFile.Save(); err != nil {
		stopAll(pidFile, out)
		return nil, err
	}
	return pidFile, nil
}

// Up runs every enabled service as a child in the foreground, for a Procfile
// line, and returns when one exits or on SIGINT and SIGTERM. When no service
// is enabled it idles instead of exiting: foreman stops every other process
// the moment one of them exits.
func Up(ctx context.Context, plan *resolve.Plan, root string, out io.Writer) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	type child struct {
		service *resolve.Service
		cmd     *exec.Cmd
	}
	var children []child
	exited := make(chan error, len(plan.Services))

	stop := func() {
		for _, c := range children {
			if c.cmd.Process != nil {
				_ = proc.Stop(proc.Started{PID: c.cmd.Process.Pid, ContainerName: c.service.ContainerName})
			}
		}
	}

	for _, service := range plan.Services {
		if !service.Enabled {
			fmt.Fprintf(out, "%s: not starting, %s.\n", service.Name, service.SkipReason)
			continue
		}
		if err := requireBinary(service); err != nil {
			fmt.Fprintf(out, "%s: not starting, %v\n", service.Name, err)
			continue
		}
		if err := prepare(service); err != nil {
			stop()
			return fmt.Errorf("%s: %w", service.Name, err)
		}
		cmd, err := proc.Child(service.Command, service.ProcEnv, root)
		if err != nil {
			stop()
			return fmt.Errorf("%s: %w", service.Name, err)
		}
		c := child{service: service, cmd: cmd}
		children = append(children, c)
		go func() {
			err := cmd.Wait()
			exited <- fmt.Errorf("%s exited: %v", c.service.Name, err)
		}()
		pid := cmd.Process.Pid
		if err := proc.WaitReady(ctx, service.ReadyURL, func() bool { return proc.Alive(pid) }); err != nil {
			stop()
			return fmt.Errorf("%s %v", service.Name, err)
		}
		if err := postStart(ctx, service); err != nil {
			stop()
			return fmt.Errorf("%s: %w", service.Name, err)
		}
		fmt.Fprintf(out, "%s: ready on port %d\n", service.Name, service.Port)
	}

	if len(children) == 0 {
		fmt.Fprintln(out, "devkit: no service to run, idling.")
	}
	select {
	case <-ctx.Done():
		stop()
		return nil
	case err := <-exited:
		stop()
		return err
	}
}

// Stop ends the servers in a pid file, removes their ephemeral directories,
// and deletes the file.
func Stop(pidFile *proc.PidFile, out io.Writer) error {
	var firstErr error
	for _, started := range pidFile.Services {
		if err := proc.Stop(started); err != nil && firstErr == nil {
			firstErr = err
		}
		if started.Ephemeral && started.Dir != "" {
			os.RemoveAll(started.Dir)
		}
	}
	if err := os.Remove(pidFile.Path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// StopStale stops the servers of every pid file in stateDir whose owner
// process is gone, and returns how many files it handled.
func StopStale(stateDir string, out io.Writer) (int, error) {
	paths, err := proc.ListPidFiles(stateDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, path := range paths {
		pidFile, err := proc.LoadPidFile(path)
		if err != nil {
			fmt.Fprintf(out, "Removing unreadable pid file %s: %v\n", path, err)
			os.Remove(path)
			continue
		}
		if pidFile.OwnerAlive() {
			continue
		}
		fmt.Fprintf(out, "Stopping servers left by pid %d (%s)...\n", pidFile.OwnerPID, filepath.Base(path))
		if err := Stop(pidFile, out); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// StopAll stops the servers of every pid file in stateDir, alive owner or
// not. `devkit complete` uses it.
func StopAll(stateDir string, out io.Writer) error {
	paths, err := proc.ListPidFiles(stateDir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		pidFile, err := proc.LoadPidFile(path)
		if err != nil {
			os.Remove(path)
			continue
		}
		fmt.Fprintf(out, "Stopping devkit services from %s...\n", filepath.Base(path))
		if err := Stop(pidFile, out); err != nil {
			return err
		}
	}
	return nil
}

// Doctor checks each enabled service's binary and prints the install hint
// for the missing ones. It returns an error when any is missing.
func Doctor(plan *resolve.Plan, out io.Writer) error {
	missing := 0
	for _, service := range plan.Services {
		if !service.Enabled {
			fmt.Fprintf(out, "%-10s skipped (%s)\n", service.Name, service.SkipReason)
			continue
		}
		path, err := exec.LookPath(service.Command[0])
		if err != nil {
			missing++
			fmt.Fprintf(out, "%-10s MISSING %s. Install: %s\n", service.Name, service.Command[0], service.Spec.InstallHint)
			continue
		}
		fmt.Fprintf(out, "%-10s ok      %s\n", service.Name, path)
	}
	if missing > 0 {
		return fmt.Errorf("%d binary(ies) missing", missing)
	}
	return nil
}

func requireBinary(service *resolve.Service) error {
	if _, err := exec.LookPath(service.Command[0]); err != nil {
		return fmt.Errorf("%s is not installed. Install it with %s", service.Command[0], service.Spec.InstallHint)
	}
	return nil
}

func stopAll(pidFile *proc.PidFile, out io.Writer) {
	_ = Stop(pidFile, out)
}
