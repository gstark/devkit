// Package gitwt discovers the git worktrees of a repository.
package gitwt

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Info describes the checkout devkit runs in and its siblings.
type Info struct {
	// Root is this checkout's top-level directory.
	Root string
	// Main is the main checkout, the first entry of `git worktree list`.
	Main string
	// All lists every worktree, Main first.
	All []string
	// CommonDir is the shared .git directory.
	CommonDir string
	// GitDir is this checkout's own git directory (.git/worktrees/<name> in a
	// linked worktree).
	GitDir string
}

// Discover runs git in dir and returns the worktree layout.
func Discover(dir string) (Info, error) {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, err
	}
	commonDir, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Info{}, err
	}
	gitDir, err := git(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Info{}, err
	}
	list, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return Info{}, err
	}
	var all []string
	for _, line := range strings.Split(list, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			all = append(all, path)
		}
	}
	if len(all) == 0 {
		return Info{}, fmt.Errorf("git worktree list returned nothing in %s", dir)
	}
	return Info{Root: root, Main: all[0], All: all, CommonDir: commonDir, GitDir: gitDir}, nil
}

// IsMain reports whether this checkout is the main one.
func (i Info) IsMain() bool {
	return samePath(i.Root, i.Main)
}

// Siblings lists the other worktrees.
func (i Info) Siblings() []string {
	var out []string
	for _, path := range i.All {
		if !samePath(path, i.Root) {
			out = append(out, path)
		}
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]`)

// Slug derives a database-safe name from the checkout's directory name:
// lower case, anything but letters and digits to "_", at most 40 characters.
func (i Info) Slug() string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(i.Root)), "_")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
