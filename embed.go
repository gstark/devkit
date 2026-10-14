package main

import (
	_ "embed"

	"github.com/gstark/devkit/internal/cmd"
)

// The adoption guide ships inside the binary, so `devkit docs adopting`
// prints the text that matches the installed version. go:embed cannot reach
// outside a package directory, so the root package embeds it and hands it to
// the command.
//
//go:embed doc/adopting.md
var adoptingDoc string

func init() {
	cmd.Docs["adopting"] = adoptingDoc
}
