// devkit gives each git worktree its own port slot, local services, and
// database. See doc/plan.md and `devkit --help`.
package main

import "github.com/gstark/devkit/internal/cmd"

func main() {
	cmd.Execute()
}
