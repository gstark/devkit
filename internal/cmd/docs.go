package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Docs holds the documents `devkit docs` can print, by name. The root package
// fills it from embedded files.
var Docs = map[string]string{}

func newDocs() *cobra.Command {
	return &cobra.Command{
		Use:   "docs [NAME]",
		Short: "Print a built-in document, such as the adoption guide (`devkit docs adopting`)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			names := make([]string, 0, len(Docs))
			for name := range Docs {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(args) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Documents: %s\nRead one with `devkit docs NAME`.\n", strings.Join(names, ", "))
				return nil
			}
			text, ok := Docs[args[0]]
			if !ok {
				return fmt.Errorf("no document named %q (have %s)", args[0], strings.Join(names, ", "))
			}
			fmt.Fprint(cmd.OutOrStdout(), text)
			return nil
		},
	}
}
