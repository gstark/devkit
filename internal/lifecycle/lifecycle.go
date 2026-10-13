// Package lifecycle implements the worktree commands: database build and
// clone, setup, and complete.
package lifecycle

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gstark/devkit/internal/db"
	"github.com/gstark/devkit/internal/envfile"
	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/lock"
	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/procs"
	"github.com/gstark/devkit/internal/services"
	"github.com/gstark/devkit/internal/slot"
	"github.com/gstark/devkit/internal/tailscale"
)

// Context carries what every lifecycle command needs.
type Context struct {
	Manifest *manifest.Manifest
	Worktree gitwt.Info
	Out      io.Writer
	Stdin    io.Reader
}

func (c Context) database() (*manifest.Database, error) {
	if c.Manifest.Database == nil {
		return nil, errors.New("the manifest has no [database] section")
	}
	return c.Manifest.Database, nil
}

// WorktreeDB is this checkout's database name.
func (c Context) WorktreeDB() string {
	return strings.ReplaceAll(c.Manifest.Database.Worktree, "{slug}", c.Worktree.Slug())
}

func (c Context) vars() manifest.Vars {
	return manifest.Vars{"root": c.Manifest.Root, "slug": c.Worktree.Slug(), "xdg_data": xdgData()}
}

func (c Context) pristineStorage() (string, error) {
	if c.Manifest.Database.PristineStorage == "" {
		return "", nil
	}
	return c.vars().Expand(c.Manifest.Database.PristineStorage)
}

func (c Context) sharedLock() (*lock.Lock, error) {
	return lock.Acquire(filepath.Join(c.Worktree.CommonDir, "devkit.lock"), "another worktree setup", c.Out)
}

// Pristine rebuilds the pristine database with the manifest's build hook and
// snapshots the storage directory. It asks first unless yes.
func (c Context) Pristine(yes bool) error {
	database, err := c.database()
	if err != nil {
		return err
	}
	if !yes {
		if err := c.confirm(fmt.Sprintf("This drops %s and rebuilds it. Continue? [y/N] ", database.Pristine)); err != nil {
			return err
		}
	}
	held, err := c.sharedLock()
	if err != nil {
		return err
	}
	defer held.Release()
	if err := c.buildPristine(); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Done. Run `devkit db clone --reset` in a worktree to copy it.")
	return nil
}

func (c Context) buildPristine() error {
	database := c.Manifest.Database
	if database.Build == "" {
		return fmt.Errorf("%s does not exist and the manifest has no database.build hook", database.Pristine)
	}
	fmt.Fprintf(c.Out, "Building %s...\n", database.Pristine)
	if err := c.runHook(database.Build, map[string]string{database.Env: database.Pristine, "DEVKIT_DATABASE": database.Pristine}); err != nil {
		return fmt.Errorf("database.build: %w", err)
	}
	pristineStorage, err := c.pristineStorage()
	if err != nil {
		return err
	}
	if database.StorageDir == "" || pristineStorage == "" {
		return nil
	}
	source := filepath.Join(c.Manifest.Root, database.StorageDir)
	if _, err := os.Stat(source); err != nil {
		fmt.Fprintf(c.Out, "No %s to snapshot.\n", database.StorageDir)
		return nil
	}
	fmt.Fprintf(c.Out, "Snapshotting %s into %s...\n", database.StorageDir, pristineStorage)
	if err := os.RemoveAll(pristineStorage); err != nil {
		return err
	}
	return copyTree(source, pristineStorage)
}

// Clone creates this worktree's database from the pristine one, clones the
// pristine storage files, and runs the migrate hook. An existing database is
// kept unless reset.
func (c Context) Clone(reset bool) error {
	if _, err := c.database(); err != nil {
		return err
	}
	if c.Worktree.IsMain() {
		return errors.New("the main checkout keeps its own database; clone runs in a linked worktree")
	}
	held, err := c.sharedLock()
	if err != nil {
		return err
	}
	defer held.Release()
	return c.cloneLocked(reset)
}

func (c Context) cloneLocked(reset bool) error {
	database := c.Manifest.Database
	client := db.Client{Out: c.Out}
	name := c.WorktreeDB()

	exists, err := client.Exists(name)
	if err != nil {
		return err
	}
	if exists && !reset {
		fmt.Fprintf(c.Out, "Keeping %s. Pass --reset to copy %s again.\n", name, database.Pristine)
	} else {
		pristineExists, err := client.Exists(database.Pristine)
		if err != nil {
			return err
		}
		if !pristineExists {
			fmt.Fprintf(c.Out, "%s does not exist.\n", database.Pristine)
			if err := c.buildPristine(); err != nil {
				return err
			}
		}
		if exists {
			if err := client.Drop(name); err != nil {
				return err
			}
		}
		fmt.Fprintf(c.Out, "Copying %s to %s...\n", database.Pristine, name)
		if err := client.CreateFromTemplate(name, database.Pristine); err != nil {
			return err
		}
		if err := c.cloneStorage(); err != nil {
			return err
		}
	}

	if err := envfile.Write(c.Manifest.EnvFilePath(c.Worktree.Root), database.Env, name); err != nil {
		return err
	}
	if database.Migrate != "" {
		fmt.Fprintln(c.Out, "Running pending migrations...")
		if err := c.runHook(database.Migrate, map[string]string{database.Env: name, "DEVKIT_DATABASE": name}); err != nil {
			return fmt.Errorf("database.migrate: %w", err)
		}
	}
	return nil
}

func (c Context) cloneStorage() error {
	database := c.Manifest.Database
	pristineStorage, err := c.pristineStorage()
	if err != nil {
		return err
	}
	if database.StorageDir == "" || pristineStorage == "" {
		return nil
	}
	if _, err := os.Stat(pristineStorage); err != nil {
		fmt.Fprintf(c.Out, "No pristine storage at %s; skipping the file clone.\n", pristineStorage)
		return nil
	}
	target := filepath.Join(c.Manifest.Root, database.StorageDir)
	fmt.Fprintf(c.Out, "Cloning the pristine storage files into %s/...\n", database.StorageDir)
	return copyTree(pristineStorage, target)
}

// Drop removes this worktree's database and its env file key. It never
// drops any other name.
func (c Context) Drop() error {
	database, err := c.database()
	if err != nil {
		return err
	}
	if c.Worktree.IsMain() {
		return errors.New("the main checkout has no worktree database to drop")
	}
	client := db.Client{Out: c.Out}
	name := c.WorktreeDB()
	envPath := c.Manifest.EnvFilePath(c.Worktree.Root)
	if configured, _ := envfile.Read(envPath, database.Env); configured != "" && configured != name {
		fmt.Fprintf(c.Out, "Leaving %s from %s. Only %s belongs to this worktree.\n", configured, c.Manifest.Project.EnvFile, name)
	}
	exists, err := client.Exists(name)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintf(c.Out, "Dropping %s...\n", name)
		if err := client.Drop(name); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(c.Out, "%s does not exist.\n", name)
	}
	return envfile.Remove(envPath, database.Env)
}

// Setup prepares a linked worktree: install, env file, database, slot.
func (c Context) Setup(reset bool) error {
	if c.Worktree.IsMain() {
		return errors.New("this is the main checkout, which keeps its own database and slot 0. Setup runs in a linked worktree")
	}
	// Two setups in one worktree (T3 Code can start the setup command twice)
	// would race in node_modules; the second waits here.
	mine, err := lock.Acquire(filepath.Join(c.Worktree.GitDir, "devkit.lock"), "another `devkit setup` in this worktree", c.Out)
	if err != nil {
		return err
	}
	defer mine.Release()

	for _, command := range c.Manifest.Setup.Install {
		fmt.Fprintf(c.Out, "Running: %s\n", command)
		if err := c.runHook(command, nil); err != nil {
			return fmt.Errorf("setup.install: %w", err)
		}
	}
	if err := c.copyEnv(); err != nil {
		return err
	}

	shared, err := c.sharedLock()
	if err != nil {
		return err
	}
	defer shared.Release()

	// The slot first: the database build hook may need this worktree's own
	// service ports, which `devkit env` reads from the env file.
	claimed, err := slot.Claim(c.Manifest, c.Worktree, c.Out)
	if err != nil {
		return err
	}
	if c.Manifest.Database != nil {
		if err := c.cloneLocked(reset); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.Out, "Done. This worktree is slot %d", claimed)
	if c.Manifest.Database != nil {
		fmt.Fprintf(c.Out, " and uses %s", c.WorktreeDB())
	}
	fmt.Fprintln(c.Out, ".")
	return nil
}

func (c Context) copyEnv() error {
	setup := c.Manifest.Setup
	ours := c.Manifest.EnvFilePath(c.Worktree.Root)
	theirs := c.Manifest.EnvFilePath(c.Worktree.Main)
	if setup.CopyEnvFile {
		if _, err := os.Stat(ours); errors.Is(err, os.ErrNotExist) {
			if content, err := os.ReadFile(theirs); err == nil {
				fmt.Fprintf(c.Out, "Copying %s from %s...\n", c.Manifest.Project.EnvFile, c.Worktree.Main)
				if err := os.WriteFile(ours, content, 0o600); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range setup.CopyEnvKeys {
		have, _ := envfile.Read(ours, key)
		if have != "" {
			continue
		}
		value, _ := envfile.Read(theirs, key)
		if value != "" {
			if err := envfile.Write(ours, key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// Complete undoes Setup: stops this checkout's servers, removes its
// Tailscale handlers, drops its database, and releases its slot.
func (c Context) Complete() error {
	if c.Worktree.IsMain() {
		return errors.New("complete runs in a linked worktree, not the main checkout")
	}
	root := c.Worktree.Root
	values, err := envfile.ReadAll(c.Manifest.EnvFilePath(root))
	if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(root, ".overmind.sock")); err == nil {
		fmt.Fprintln(c.Out, "Stopping overmind...")
		cmd := exec.Command("overmind", "quit", "--socket", ".overmind.sock")
		cmd.Dir = root
		_ = cmd.Run()
	}
	if err := services.StopAll(c.Manifest.StatePath(), c.Out); err != nil {
		return err
	}
	var pidFiles []string
	for _, path := range c.Manifest.Complete.PidFiles {
		pidFiles = append(pidFiles, filepath.Join(root, path))
	}
	matches, err := procs.InCheckout(root, c.Manifest.Complete.ProcessPatterns, pidFiles)
	if err != nil {
		return err
	}
	if err := procs.StopAll(matches, c.Out); err != nil {
		return err
	}
	for _, path := range pidFiles {
		os.Remove(path)
	}
	os.Remove(filepath.Join(root, ".overmind.sock"))

	if tailscale.Available() {
		for _, name := range c.Manifest.PortNames() {
			port, err := portValue(values[c.Manifest.Ports[name].Env])
			if err != nil {
				continue
			}
			if err := tailscale.Off(port, c.Out); err != nil {
				fmt.Fprintf(c.Out, "Could not read tailscale serve status: %v\n", err)
			}
		}
	}

	if c.Manifest.Database != nil {
		if err := c.Drop(); err != nil {
			return err
		}
	}
	if err := slot.Release(c.Manifest, c.Worktree); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Done. Stopped this worktree's servers and released its slot.")
	return nil
}

func (c Context) runHook(command string, env map[string]string) error {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = c.Manifest.Root
	cmd.Stdin = os.Stdin
	cmd.Stdout = c.Out
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd.Run()
}

func (c Context) confirm(prompt string) error {
	stdin, isFile := c.Stdin.(*os.File)
	if !isFile || !isTerminal(stdin) {
		return errors.New("pass --yes to run this from a non-interactive terminal")
	}
	fmt.Fprint(c.Out, prompt)
	answer, _ := bufio.NewReader(c.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return errors.New("aborted")
	}
	return nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// copyTree copies the contents of source into target, copy-on-write where
// the file system supports it (APFS clones on macOS, reflinks on Linux).
func copyTree(source, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("/bin/cp", "-c", "-R", source+"/.", target+"/")
	} else {
		cmd = exec.Command("/bin/cp", "--reflink=auto", "-R", source+"/.", target+"/")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy %s to %s: %s", source, target, strings.TrimSpace(string(out)))
	}
	return nil
}

func portValue(raw string) (int, error) {
	var port int
	_, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &port)
	if err != nil || port <= 0 {
		return 0, errors.New("no port")
	}
	return port, nil
}

func xdgData() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}
