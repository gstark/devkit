package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const sample = `
[project]
name = "sample"

[ports.spa]
default = 3000
env = "SPA_PORT"

[services.s3]
preset = "rustfs"
default_port = 9000
bucket = "sample-dev"
extra_env = { BUCKET = "{bucket}" }

[services.s3.profiles.test]
bucket = "sample-test"
buckets = ["sample-test", "sample-import"]

[services.livekit]
preset = "livekit"
default_port = 7880
webhook_url = "http://localhost:{ports.rails}/hooks"

[services.livekit.profiles.test]
webhook_url = ""
loopback_only = true

[services.custom]
default_port = 3005
ports = { tcp = 1 }
command = ["sleep", "{port}"]
ready_url = "http://127.0.0.1:{port}/health"
`

func TestLoadAppliesDefaultsAndMergesPresets(t *testing.T) {
	m, err := Load(writeManifest(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if m.Project.EnvFile != ".env.development.local" || m.Project.StateDir != "tmp/devkit" {
		t.Errorf("defaults not applied: %+v", m.Project)
	}

	s3 := m.Resolve("s3", "dev")
	if s3.Command[0] != "rustfs" || s3.Ports.TCP != 1 || s3.ReadyURL == "" {
		t.Errorf("preset not merged: %+v", s3)
	}
	if s3.Bucket != "sample-dev" || s3.ExtraEnv["BUCKET"] != "{bucket}" || s3.ExtraEnv["AWS_ENDPOINT_URL_S3"] == "" {
		t.Errorf("manifest overrides lost: %+v", s3)
	}
	if s3.Profiles != nil {
		t.Error("resolved service must not carry profiles")
	}

	test := m.Resolve("s3", "test")
	if test.Bucket != "sample-test" || len(test.Buckets) != 2 {
		t.Errorf("profile not applied: %+v", test)
	}
	if m.Resolve("s3", "dev").Buckets != nil {
		t.Error("profile leaked into dev")
	}

	lk := m.Resolve("livekit", "dev")
	if lk.WebhookURL == nil || *lk.WebhookURL == "" || lk.LoopbackOnly != nil {
		t.Errorf("dev livekit: %+v", lk)
	}
	lkTest := m.Resolve("livekit", "test")
	if lkTest.WebhookURL == nil || *lkTest.WebhookURL != "" {
		t.Error("test profile must clear the webhook with an empty string")
	}
	if lkTest.LoopbackOnly == nil || !*lkTest.LoopbackOnly {
		t.Error("test profile must set loopback_only")
	}
}

func TestLoadRejectsUnknownKeysAndMissingFields(t *testing.T) {
	cases := map[string]string{
		"unknown keys":  "[project]\nname = \"x\"\nbogus = 1\n",
		"no name":       "[project]\nenv_file = \".env\"\n",
		"bad preset":    "[project]\nname = \"x\"\n[services.a]\npreset = \"nope\"\ndefault_port = 1\n",
		"no command":    "[project]\nname = \"x\"\n[services.a]\ndefault_port = 1\nports = { tcp = 1 }\nready_url = \"http://x\"\n",
		"no port":       "[project]\nname = \"x\"\n[services.a]\npreset = \"rustfs\"\n",
		"no slug in db": "[project]\nname = \"x\"\n[database]\nenv = \"DB\"\npristine = \"p\"\nworktree = \"w\"\n",
		"bad ts port":   "[project]\nname = \"x\"\n[tailscale]\nflag = \"F\"\npublish = \"nope\"\n",
	}
	for name, content := range cases {
		if _, err := Load(writeManifest(t, content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestFindWalksUp(t *testing.T) {
	root := writeManifest(t, "[project]\nname = \"x\"\n")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := Find(nested)
	if err != nil {
		t.Fatal(err)
	}
	if found != root {
		t.Errorf("found %s, want %s", found, root)
	}
	if _, err := Find(t.TempDir()); err == nil || !strings.Contains(err.Error(), FileName) {
		t.Errorf("expected a not-found error, got %v", err)
	}
}

func TestExpand(t *testing.T) {
	vars := Vars{"port": "7880", "ports.rails": "3001", "bucket": "b"}
	cases := map[string]string{
		"port: {port}":              "port: 7880",
		"tcp {port+1} udp {port+2}": "tcp 7881 udp 7882",
		"http://x:{ports.rails}/h":  "http://x:3001/h",
		"{bucket}":                  "b",
		"plain":                     "plain",
	}
	for in, want := range cases {
		got, err := vars.Expand(in)
		if err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := vars.Expand("{nope}"); err == nil {
		t.Error("unknown variable must fail")
	}
	if _, err := vars.Expand("{bucket+1}"); err == nil {
		t.Error("arithmetic on a non-number must fail")
	}
	with := vars.With("extra", "1")
	if _, ok := vars["extra"]; ok {
		t.Error("With must not mutate the receiver")
	}
	if with["extra"] != "1" {
		t.Error("With must add the key")
	}
}
