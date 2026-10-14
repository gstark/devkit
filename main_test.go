package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDocsAdoptingIsEmbedded(t *testing.T) {
	out, err := exec.Command("go", "run", ".", "docs", "adopting").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# Adopting devkit in a repository") {
		t.Errorf("unexpected start: %q", string(out[:60]))
	}
}
