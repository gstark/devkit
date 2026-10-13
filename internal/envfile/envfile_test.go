package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRemovePreserveOtherLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# comment\nexport KEEP=\"quoted\"\nA=1\nA=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if value, _ := Read(path, "A"); value != "2" {
		t.Errorf("last line wins: got %q", value)
	}
	if value, _ := Read(path, "KEEP"); value != "quoted" {
		t.Errorf("quotes and export stripped: got %q", value)
	}
	if value, _ := Read(path, "MISSING"); value != "" {
		t.Errorf("missing key: got %q", value)
	}

	if err := Write(path, "A", "3"); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]string{"B": "x", "C": "y"}, nil); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	want := "# comment\nexport KEEP=\"quoted\"\nA=3\nB=x\nC=y\n"
	if string(content) != want {
		t.Errorf("content:\n%s\nwant:\n%s", content, want)
	}

	if err := Remove(path, "A", "C", "NOPE"); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if string(content) != "# comment\nexport KEEP=\"quoted\"\nB=x\n" {
		t.Errorf("after remove:\n%s", content)
	}
}

func TestMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none")
	if value, err := Read(path, "A"); err != nil || value != "" {
		t.Errorf("Read on a missing file: %q, %v", value, err)
	}
	if err := Remove(path, "A"); err != nil {
		t.Errorf("Remove on a missing file: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("Remove must not create the file")
	}
	if err := Write(path, "A", "1"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "A=1\n" {
		t.Errorf("content %q", content)
	}
}
