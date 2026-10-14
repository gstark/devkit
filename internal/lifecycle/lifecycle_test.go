package lifecycle

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gstark/devkit/internal/db"
	"github.com/gstark/devkit/internal/envfile"
	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
)

// The lifecycle tests need a local Postgres the current user can reach with
// psql, and skip otherwise. They name their databases devkit_lifecycle_test_*
// and drop them.
func requirePostgres(t *testing.T) {
	t.Helper()
	if err := exec.Command("psql", "-X", "-qtA", "-d", "postgres", "-c", "select 1").Run(); err != nil {
		t.Skip("no local Postgres reachable with psql")
	}
}

func repoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(main, "init", "-q")
	run(main, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(base, "feature-x")
	run(main, "worktree", "add", "-q", worktree, "-b", "feature")
	return main, worktree
}

func TestCloneDropAndComplete(t *testing.T) {
	requirePostgres(t)
	_, worktree := repoWithWorktree(t)
	wt, err := gitwt.Discover(worktree)
	if err != nil {
		t.Fatal(err)
	}
	pristineStorage := filepath.Join(t.TempDir(), "pristine-storage")
	m := &manifest.Manifest{
		Root:    worktree,
		Project: manifest.Project{Name: "lifecycle", EnvFile: ".env.test.local", StateDir: "tmp/devkit"},
		Ports:   map[string]manifest.Port{"app": {Default: 43000, Env: "APP_PORT"}},
		Database: &manifest.Database{
			Env:      "TEST_DATABASE",
			Pristine: "devkit_lifecycle_test_pristine",
			Worktree: "devkit_lifecycle_test_wt_{slug}",
			// The hook sees the slot's port: setup claims the slot before it builds.
			Build: `test "$(sed -n 's/^APP_PORT=//p' .env.test.local)" = 43010 && ` +
				`psql -X -q -d postgres -c "drop database if exists \"$DEVKIT_DATABASE\" with (force)" && createdb "$DEVKIT_DATABASE" ` +
				`&& psql -X -q -d "$DEVKIT_DATABASE" -c "create table marks(id int); insert into marks values (1), (2)" ` +
				`&& mkdir -p storage && echo sample > storage/sample.txt`,
			Migrate:         `echo "migrated $TEST_DATABASE" > tmp/migrated`,
			StorageDir:      "storage",
			PristineStorage: pristineStorage,
		},
		Setup:    manifest.Setup{Install: []string{"mkdir -p tmp && echo installed > tmp/installed"}, Env: map[string]string{"LOGIN": "seed@example.com"}},
		Complete: manifest.Complete{ProcessPatterns: []string{"sleep 7654321"}},
	}
	client := db.Client{Out: &bytes.Buffer{}}
	worktreeDB := "devkit_lifecycle_test_wt_feature_x"
	t.Cleanup(func() {
		_ = client.Drop(worktreeDB)
		_ = client.Drop(m.Database.Pristine)
	})

	var out bytes.Buffer
	lc := Context{Manifest: m, Worktree: wt, Out: &out, Stdin: strings.NewReader("")}

	if err := lc.Setup(false); err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(worktree, "tmp/installed")); err != nil {
		t.Error("install hook did not run")
	}
	migrated, _ := os.ReadFile(filepath.Join(worktree, "tmp/migrated"))
	if strings.TrimSpace(string(migrated)) != "migrated "+worktreeDB {
		t.Errorf("migrate hook saw %q", migrated)
	}
	if _, err := os.Stat(filepath.Join(worktree, "storage/sample.txt")); err != nil {
		t.Error("storage not cloned from the pristine snapshot")
	}
	rows, err := exec.Command("psql", "-X", "-qtA", "-d", worktreeDB, "-c", "select count(*) from marks").Output()
	if err != nil || strings.TrimSpace(string(rows)) != "2" {
		t.Errorf("clone has %q marks, %v", rows, err)
	}
	envPath := filepath.Join(worktree, ".env.test.local")
	if value, _ := envfile.Read(envPath, "TEST_DATABASE"); value != worktreeDB {
		t.Errorf("env file database %q", value)
	}
	if value, _ := envfile.Read(envPath, "APP_PORT"); value != "43010" {
		t.Errorf("env file port %q", value)
	}
	if value, _ := envfile.Read(envPath, "LOGIN"); value != "seed@example.com" {
		t.Errorf("setup.env not written: %q", value)
	}

	// A second setup keeps the database: drop a row, then check it stays gone.
	if err := exec.Command("psql", "-X", "-q", "-d", worktreeDB, "-c", "delete from marks where id = 1").Run(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := lc.Setup(false); err != nil {
		t.Fatalf("second setup: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Keeping "+worktreeDB) {
		t.Errorf("second setup must keep the database:\n%s", out.String())
	}
	if err := lc.Setup(true); err != nil {
		t.Fatalf("reset: %v", err)
	}
	rows, _ = exec.Command("psql", "-X", "-qtA", "-d", worktreeDB, "-c", "select count(*) from marks").Output()
	if strings.TrimSpace(string(rows)) != "2" {
		t.Errorf("reset must copy the pristine database again: %q", rows)
	}

	// A stray process from inside the worktree, which complete must stop.
	stray := exec.Command("sleep", "7654321")
	stray.Dir = worktree
	if err := stray.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stray.Process.Kill() }()

	out.Reset()
	if err := lc.Complete(); err != nil {
		t.Fatalf("complete: %v\n%s", err, out.String())
	}
	if exists, _ := client.Exists(worktreeDB); exists {
		t.Error("complete must drop the worktree database")
	}
	if exists, _ := client.Exists(m.Database.Pristine); !exists {
		t.Error("complete must keep the pristine database")
	}
	values, _ := envfile.ReadAll(envPath)
	delete(values, "LOGIN") // setup.env keys stay; they are harmless
	if len(values) != 0 {
		t.Errorf("env keys left: %v", values)
	}
	if err := stray.Wait(); err == nil {
		t.Error("stray process should have been signalled")
	}
}

func TestSetupRefusesMainCheckout(t *testing.T) {
	main, _ := repoWithWorktree(t)
	wt, _ := gitwt.Discover(main)
	lc := Context{Manifest: &manifest.Manifest{Root: main, Project: manifest.Project{Name: "x", EnvFile: ".env"}}, Worktree: wt, Out: &bytes.Buffer{}}
	if err := lc.Setup(false); err == nil {
		t.Error("setup must refuse the main checkout")
	}
	if err := lc.Complete(); err == nil {
		t.Error("complete must refuse the main checkout")
	}
}
