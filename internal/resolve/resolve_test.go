package resolve

import (
	"testing"

	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
)

func str(s string) *string { return &s }

func sampleManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Root:    "/repo",
		Project: manifest.Project{Name: "sample", EnvFile: ".env", StateDir: "tmp/devkit"},
		Ports: map[string]manifest.Port{
			"spa":   {Default: 3000, Env: "SPA_PORT"},
			"rails": {Default: 3001, Env: "RAILS_PORT", ExtraEnv: map[string]string{"RAILS_ORIGIN": "http://localhost:{port}"}},
		},
		Services: map[string]manifest.Service{
			"s3": {Preset: manifest.PresetRustfs, DefaultPort: 9000, SkipIfSet: "AWS_ENDPOINT_URL_S3", Bucket: "dev",
				ExtraEnv: map[string]string{"BUCKET": "{bucket}"},
				Profiles: map[string]manifest.Service{"acceptance": {DefaultPort: 4715, Bucket: "acc"}}},
			"livekit": {Preset: manifest.PresetLivekit, DefaultPort: 7880, SkipIfSet: "LIVEKIT_URL",
				WebhookURL: str("http://localhost:{ports.rails}/hooks"),
				ExtraEnv:   map[string]string{"LIVEKIT_CLIENT_URL": "{origin.spa}"},
				Profiles:   map[string]manifest.Service{"test": {WebhookURL: str(""), ExtraEnv: map[string]string{"LIVEKIT_CLIENT_URL": ""}}}},
		},
		Database: &manifest.Database{Env: "DEV_DATABASE", Pristine: "p", Worktree: "wt_{slug}"},
		Profiles: map[string]manifest.Profile{
			"acceptance": {Ports: map[string]int{"spa": 4710, "rails": 4711}, Database: "sample_acceptance"},
		},
	}
}

func resolveWith(t *testing.T, o Options) *Plan {
	t.Helper()
	o.Manifest = sampleManifest()
	o.Worktree = gitwt.Info{Root: "/repos/My-Feature", Main: "/repos/main"}
	if o.ShellEnv == nil {
		o.ShellEnv = map[string]string{}
	}
	plan, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestSlotMovesEveryPort(t *testing.T) {
	plan := resolveWith(t, Options{Slot: 2})
	if plan.Ports["spa"] != 3020 || plan.Ports["rails"] != 3021 {
		t.Errorf("named ports: %v", plan.Ports)
	}
	if plan.Env["SPA_PORT"] != "3020" || plan.Env["RAILS_ORIGIN"] != "http://localhost:3021" {
		t.Errorf("port env: %v", plan.Env)
	}
	byName := map[string]*Service{}
	for _, s := range plan.Services {
		byName[s.Name] = s
	}
	if byName["s3"].Port != 9020 || byName["livekit"].Port != 7900 {
		t.Errorf("service ports: s3=%d livekit=%d", byName["s3"].Port, byName["livekit"].Port)
	}
	if plan.Env["AWS_ENDPOINT_URL_S3"] != "http://127.0.0.1:9020" || plan.Env["LIVEKIT_URL"] != "ws://127.0.0.1:7900" {
		t.Errorf("service env: %v", plan.Env)
	}
	if plan.Env["LIVEKIT_WEBHOOK_URL"] != "http://localhost:3021/hooks" {
		t.Errorf("webhook: %q", plan.Env["LIVEKIT_WEBHOOK_URL"])
	}
	if plan.Env["LIVEKIT_CLIENT_URL"] != "http://localhost:3020" {
		t.Errorf("origin default: %q", plan.Env["LIVEKIT_CLIENT_URL"])
	}
	if plan.Env["BUCKET"] != "dev" || plan.Env[SlotKey] != "2" || plan.Env[EnabledServicesKey] != "livekit,s3" {
		t.Errorf("env: %v", plan.Env)
	}
	if len(plan.Unset) != 1 || plan.Unset[0] != "AWS_SESSION_TOKEN" {
		t.Errorf("unset: %v", plan.Unset)
	}
}

func TestPortPrecedence(t *testing.T) {
	plan := resolveWith(t, Options{Slot: 1, EnvFile: map[string]string{"SPA_PORT": "3050"}})
	if plan.Ports["spa"] != 3050 || plan.Ports["rails"] != 3011 {
		t.Errorf("env file beats slot: %v", plan.Ports)
	}
	plan = resolveWith(t, Options{Slot: 1, EnvFile: map[string]string{"SPA_PORT": "3050"}, ShellEnv: map[string]string{"SPA_PORT": "3060"}})
	if plan.Ports["spa"] != 3060 {
		t.Errorf("shell beats env file: %v", plan.Ports)
	}
	plan = resolveWith(t, Options{Slot: 1, Profile: "acceptance", ShellEnv: map[string]string{"SPA_PORT": "3060"}})
	if plan.Ports["spa"] != 4720 || plan.Ports["rails"] != 4721 {
		t.Errorf("profile ports beat the shell and follow the slot: %v", plan.Ports)
	}
}

func TestProfileOverridesServicesAndDatabase(t *testing.T) {
	plan := resolveWith(t, Options{Profile: "acceptance", EnvFile: map[string]string{"DEV_DATABASE": "from_file"}})
	if plan.Database != "sample_acceptance" || plan.Env["DEV_DATABASE"] != "sample_acceptance" {
		t.Errorf("profile database: %q", plan.Database)
	}
	for _, s := range plan.Services {
		if s.Name == "s3" && (s.Port != 4715 || plan.Env["BUCKET"] != "acc") {
			t.Errorf("s3 acceptance: port %d bucket %q", s.Port, plan.Env["BUCKET"])
		}
	}

	plan = resolveWith(t, Options{EnvFile: map[string]string{"DEV_DATABASE": "from_file"}})
	if plan.Database != "from_file" {
		t.Errorf("env file database: %q", plan.Database)
	}
	plan = resolveWith(t, Options{})
	if plan.Database != "" {
		t.Errorf("main checkout exports no database: %q", plan.Database)
	}
	if _, ok := plan.Env["DEV_DATABASE"]; ok {
		t.Error("DEV_DATABASE must be absent")
	}
}

func TestTestProfileClearsWebhookAndClientURL(t *testing.T) {
	plan := resolveWith(t, Options{Profile: "test"})
	for _, key := range []string{"LIVEKIT_WEBHOOK_URL", "LIVEKIT_CLIENT_URL"} {
		if _, ok := plan.Env[key]; ok {
			t.Errorf("%s must not be exported", key)
		}
		found := false
		for _, unset := range plan.Unset {
			found = found || unset == key
		}
		if !found {
			t.Errorf("%s must be in Unset: %v", key, plan.Unset)
		}
	}
}

func TestSkipIfSetAndEnabledList(t *testing.T) {
	plan := resolveWith(t, Options{ShellEnv: map[string]string{"AWS_ENDPOINT_URL_S3": "http://real-s3"}})
	for _, s := range plan.Services {
		if s.Name == "s3" && (s.Enabled || s.SkipReason == "") {
			t.Errorf("s3 must be skipped: %+v", s)
		}
	}
	if _, ok := plan.Env["AWS_ACCESS_KEY_ID"]; ok {
		t.Error("a skipped service exports nothing")
	}
	if plan.Env[EnabledServicesKey] != "livekit" {
		t.Errorf("enabled list: %q", plan.Env[EnabledServicesKey])
	}

	// A later devkit call under the eval'd env sees AWS_ENDPOINT_URL_S3 set by
	// devkit itself; the enabled list wins over skip_if_set.
	plan = resolveWith(t, Options{ShellEnv: map[string]string{"AWS_ENDPOINT_URL_S3": "http://127.0.0.1:9000", EnabledServicesKey: "livekit,s3"}})
	for _, s := range plan.Services {
		if !s.Enabled {
			t.Errorf("%s must be enabled through %s", s.Name, EnabledServicesKey)
		}
	}
}

func TestOrigins(t *testing.T) {
	m := sampleManifest()
	m.Tailscale = &manifest.Tailscale{Flag: "TS", Publish: "spa", OriginEnv: "SPA_ORIGIN"}
	plan, err := Resolve(Options{Manifest: m, ShellEnv: map[string]string{}, Origins: map[string]string{"spa": "https://mac.tail.ts.net:3000"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["SPA_ORIGIN"] != "https://mac.tail.ts.net:3000" || plan.Env["LIVEKIT_CLIENT_URL"] != "https://mac.tail.ts.net:3000" {
		t.Errorf("origin env: %v", plan.Env)
	}
}

func TestEphemeralPicksFreePortsAndTempDirs(t *testing.T) {
	plan := resolveWith(t, Options{Profile: "test", Ephemeral: true})
	for _, s := range plan.Services {
		if s.Port < 20_000 || s.DataDir == "" || s.ConfigPath == "" {
			t.Errorf("%s: port %d dir %q config %q", s.Name, s.Port, s.DataDir, s.ConfigPath)
		}
	}
}

func TestContainerNameIsATemplateVariable(t *testing.T) {
	m := sampleManifest()
	m.Services["gotenberg"] = manifest.Service{
		DefaultPort: 3005, Ports: manifest.PortCount{TCP: 1},
		ContainerName: "app-gotenberg-{port}",
		Command:       []string{"docker", "run", "--name", "{container_name}", "img"},
		ReadyURL:      "http://127.0.0.1:{port}/health",
	}
	plan, err := Resolve(Options{Manifest: m, Slot: 1, ShellEnv: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Services {
		if s.Name == "gotenberg" && (s.ContainerName != "app-gotenberg-3015" || s.Command[3] != "app-gotenberg-3015") {
			t.Errorf("container name: %q command %v", s.ContainerName, s.Command)
		}
	}
}

func TestServicePortsAreTemplateVariables(t *testing.T) {
	m := sampleManifest()
	yes := true
	m.Services["gotenberg"] = manifest.Service{
		DefaultPort: 3005, Ports: manifest.PortCount{TCP: 1},
		Command:  []string{"gotenberg", "--allow={ports.spa}|{services.s3.port}"},
		ReadyURL: "http://127.0.0.1:{port}/health",
	}
	m.Services["s3"] = manifest.Service{Preset: manifest.PresetRustfs, DefaultPort: 9000, PublicRead: &yes}
	plan, err := Resolve(Options{Manifest: m, Slot: 1, ShellEnv: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Services {
		if s.Name == "gotenberg" && s.Command[1] != "--allow=3010|9010" {
			t.Errorf("command %v", s.Command)
		}
		if s.Name == "s3" && !s.PublicRead {
			t.Error("public_read not carried")
		}
	}
}
