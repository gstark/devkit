package slot

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gstark/devkit/internal/envfile"
	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
)

// repoWithWorktrees creates a git repository with n linked worktrees and
// returns the main checkout and the worktrees.
func repoWithWorktrees(t *testing.T, n int) (string, []string) {
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
	var worktrees []string
	for i := 0; i < n; i++ {
		path := filepath.Join(base, "wt"+string(rune('a'+i)))
		run(main, "worktree", "add", "-q", path, "-b", "branch"+string(rune('a'+i)))
		worktrees = append(worktrees, path)
	}
	return main, worktrees
}

func testManifest(root string) *manifest.Manifest {
	// Unusual defaults, so a developer's own servers on 3000-ish ports do not
	// make the slot search skip.
	return &manifest.Manifest{
		Root:    root,
		Project: manifest.Project{Name: "t", EnvFile: ".env.test.local"},
		Ports:   map[string]manifest.Port{"app": {Default: 41000, Env: "APP_PORT"}},
		Services: map[string]manifest.Service{
			"svc": {DefaultPort: 42000, Ports: manifest.PortCount{TCP: 1}, Command: []string{"x"}, ReadyURL: "http://x"},
		},
	}
}

func TestClaimCurrentRelease(t *testing.T) {
	main, worktrees := repoWithWorktrees(t, 2)
	m := testManifest(main)

	mainInfo, err := gitwt.Discover(main)
	if err != nil {
		t.Fatal(err)
	}
	if !mainInfo.IsMain() {
		t.Fatal("main checkout not detected")
	}
	if current, err := Current(m, mainInfo); err != nil || current != 0 {
		t.Errorf("main is slot 0: %d, %v", current, err)
	}
	if _, err := Claim(m, mainInfo, &bytes.Buffer{}); err == nil {
		t.Error("claim must refuse in the main checkout")
	}

	first, err := gitwt.Discover(worktrees[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Current(m, first); !errors.Is(err, ErrUnclaimed) {
		t.Errorf("unclaimed worktree: %v", err)
	}
	slotA, err := Claim(m, first, &bytes.Buffer{})
	if err != nil || slotA != 1 {
		t.Fatalf("first claim: %d, %v", slotA, err)
	}
	if value, _ := envfile.Read(filepath.Join(worktrees[0], ".env.test.local"), "APP_PORT"); value != "41010" {
		t.Errorf("port written: %q", value)
	}
	if again, _ := Claim(m, first, &bytes.Buffer{}); again != 1 {
		t.Errorf("claim is idempotent: %d", again)
	}

	second, _ := gitwt.Discover(worktrees[1])
	slotB, err := Claim(m, second, &bytes.Buffer{})
	if err != nil || slotB != 2 {
		t.Fatalf("second claim must skip the sibling's slot: %d, %v", slotB, err)
	}

	if err := Release(m, first); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(m, first); !errors.Is(err, ErrUnclaimed) {
		t.Error("release must clear the slot")
	}
	// Derived from a port on a slot boundary when DEVKIT_SLOT is missing.
	if err := envfile.Write(filepath.Join(worktrees[0], ".env.test.local"), "APP_PORT", "41030"); err != nil {
		t.Fatal(err)
	}
	if current, err := Current(m, first); err != nil || current != 3 {
		t.Errorf("derived slot: %d, %v", current, err)
	}
}

func TestSlug(t *testing.T) {
	info := gitwt.Info{Root: "/x/My Feature-Branch.v2"}
	if info.Slug() != "my_feature_branch_v2" {
		t.Errorf("slug %q", info.Slug())
	}
}
