package services

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/proc"
	"github.com/gstark/devkit/internal/resolve"
)

// The integration tests run against the real binaries and skip when one is
// not on PATH. CI installs them with mise.
func skipWithout(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not on PATH", name)
	}
}

func ephemeralPlan(t *testing.T, services map[string]manifest.Service) *resolve.Plan {
	t.Helper()
	root := t.TempDir()
	m := &manifest.Manifest{
		Root:     root,
		Project:  manifest.Project{Name: "t", EnvFile: ".env", StateDir: "tmp/devkit"},
		Ports:    map[string]manifest.Port{"rails": {Default: 3001, Env: "RAILS_PORT"}},
		Services: services,
	}
	plan, err := resolve.Resolve(resolve.Options{
		Manifest: m, Worktree: gitwt.Info{Root: root, Main: root}, Profile: "test", Ephemeral: true, ShellEnv: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRustfsStartCreatesBucketsAndStopCleansUp(t *testing.T) {
	skipWithout(t, "rustfs")
	yes := true
	plan := ephemeralPlan(t, map[string]manifest.Service{
		"s3": {Preset: manifest.PresetRustfs, DefaultPort: 9000, Buckets: []string{"one", "two"}, PublicRead: &yes},
	})
	pidPath := filepath.Join(plan.Services[0].DataDir, "..", "s3.pids.json")
	var out bytes.Buffer
	pidFile, err := Start(context.Background(), plan, t.TempDir(), pidPath, StartOptions{}, &out)
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out.String())
	}
	service := plan.Services[0]

	client, err := S3Client(context.Background(), plan.Env["AWS_ENDPOINT_URL_S3"])
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := client.ListBuckets(context.Background(), &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	if len(buckets.Buckets) != 2 {
		t.Errorf("buckets: %d", len(buckets.Buckets))
	}
	cors, err := client.GetBucketCors(context.Background(), &s3.GetBucketCorsInput{Bucket: &service.Buckets[0]})
	if err != nil || len(cors.CORSRules) != 1 || cors.CORSRules[0].ExposeHeaders[0] != "ETag" {
		t.Errorf("cors: %v %+v", err, cors)
	}
	policy, err := client.GetBucketPolicy(context.Background(), &s3.GetBucketPolicyInput{Bucket: &service.Buckets[0]})
	if err != nil || !strings.Contains(*policy.Policy, "s3:GetObject") {
		t.Errorf("public read policy: %v %v", err, policy)
	}
	if pidFile.OwnerPID != os.Getppid() {
		t.Errorf("owner defaults to the parent: %d", pidFile.OwnerPID)
	}

	if err := Stop(pidFile, &out); err != nil {
		t.Fatal(err)
	}
	if proc.Alive(pidFile.Services[0].PID) {
		t.Error("rustfs still runs after stop")
	}
	if _, err := os.Stat(service.DataDir); !os.IsNotExist(err) {
		t.Error("ephemeral data dir must be removed")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pid file must be removed")
	}
}

func TestLivekitStartWritesConfig(t *testing.T) {
	skipWithout(t, "livekit-server")
	loopback := true
	plan := ephemeralPlan(t, map[string]manifest.Service{
		"livekit": {Preset: manifest.PresetLivekit, DefaultPort: 7880, LoopbackOnly: &loopback},
	})
	var out bytes.Buffer
	pidFile, err := Start(context.Background(), plan, t.TempDir(), filepath.Join(t.TempDir(), "lk.pids.json"), StartOptions{}, &out)
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out.String())
	}
	defer Stop(pidFile, &out)
	config, err := os.ReadFile(plan.Services[0].ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bind_addresses: [127.0.0.1]", "enable_loopback_candidate: true", manifest.LivekitAPIKey + ": " + manifest.LivekitAPISecret} {
		if !bytes.Contains(config, []byte(want)) {
			t.Errorf("config lacks %q:\n%s", want, config)
		}
	}
	if bytes.Contains(config, []byte("webhook:")) {
		t.Error("no webhook without a webhook_url")
	}
}

func TestStopStaleOnlyTouchesDeadOwners(t *testing.T) {
	skipWithout(t, "rustfs")
	plan := ephemeralPlan(t, map[string]manifest.Service{
		"s3": {Preset: manifest.PresetRustfs, DefaultPort: 9000},
	})
	stateDir := t.TempDir()
	var out bytes.Buffer
	alive, err := Start(context.Background(), plan, t.TempDir(), filepath.Join(stateDir, "alive.pids.json"), StartOptions{Owner: os.Getpid()}, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(alive, &out)

	plan2 := ephemeralPlan(t, map[string]manifest.Service{"s3": {Preset: manifest.PresetRustfs, DefaultPort: 9000}})
	dead, err := Start(context.Background(), plan2, t.TempDir(), filepath.Join(stateDir, "dead.pids.json"), StartOptions{Owner: 999_999}, &out)
	if err != nil {
		t.Fatal(err)
	}

	count, err := StopStale(stateDir, &out)
	if err != nil || count != 1 {
		t.Fatalf("stale count %d, %v", count, err)
	}
	if proc.Alive(dead.Services[0].PID) {
		t.Error("orphaned server must be stopped")
	}
	if !proc.Alive(alive.Services[0].PID) {
		t.Error("owned server must keep running")
	}
}

func TestDoctorReportsMissingBinary(t *testing.T) {
	plan := ephemeralPlan(t, map[string]manifest.Service{
		"ghost": {DefaultPort: 5000, Ports: manifest.PortCount{TCP: 1}, Command: []string{"devkit-no-such-binary"}, ReadyURL: "http://127.0.0.1:{port}/", InstallHint: "nothing"},
	})
	var out bytes.Buffer
	if err := Doctor(plan, &out); err == nil {
		t.Error("doctor must fail for a missing binary")
	}
	if !bytes.Contains(out.Bytes(), []byte("MISSING")) || !bytes.Contains(out.Bytes(), []byte("nothing")) {
		t.Errorf("doctor output:\n%s", out.String())
	}
}

func TestReuseSkipsARunningServerAndOnlyLimitsTheStart(t *testing.T) {
	skipWithout(t, "rustfs")
	plan := ephemeralPlan(t, map[string]manifest.Service{
		"s3":    {Preset: manifest.PresetRustfs, DefaultPort: 9000},
		"ghost": {DefaultPort: 5000, Ports: manifest.PortCount{TCP: 1}, Command: []string{"devkit-no-such-binary"}, ReadyURL: "http://127.0.0.1:{port}/"},
	})
	var out bytes.Buffer
	first, err := Start(context.Background(), plan, t.TempDir(), filepath.Join(t.TempDir(), "a.pids.json"), StartOptions{Only: []string{"s3"}}, &out)
	if err != nil {
		t.Fatalf("start with --only: %v\n%s", err, out.String())
	}
	defer Stop(first, &out)
	if len(first.Services) != 1 || first.Services[0].Name != "s3" {
		t.Fatalf("only s3 should start: %+v", first.Services)
	}

	// The same plan again, so the same port: without Reuse rustfs cannot bind.
	second, err := Start(context.Background(), plan, t.TempDir(), filepath.Join(t.TempDir(), "b.pids.json"), StartOptions{Only: []string{"s3"}, Reuse: true}, &out)
	if err != nil {
		t.Fatalf("start with --reuse: %v\n%s", err, out.String())
	}
	if len(second.Services) != 0 {
		t.Errorf("a reused server must not be recorded: %+v", second.Services)
	}
	if !bytes.Contains(out.Bytes(), []byte("reusing it")) {
		t.Errorf("output:\n%s", out.String())
	}
	if err := Stop(second, &out); err != nil {
		t.Fatal(err)
	}
	if !proc.Alive(first.Services[0].PID) {
		t.Error("stopping the reusing run must leave the original server alone")
	}
}
