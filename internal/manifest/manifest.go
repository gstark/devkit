// Package manifest loads devkit.toml and resolves service definitions
// against the built-in presets and the requested profile.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the manifest file devkit looks for at the repository root.
const FileName = "devkit.toml"

// Manifest is the parsed devkit.toml.
type Manifest struct {
	Project   Project            `toml:"project"`
	Ports     map[string]Port    `toml:"ports"`
	Services  map[string]Service `toml:"services"`
	Database  *Database          `toml:"database"`
	Setup     Setup              `toml:"setup"`
	Tailscale *Tailscale         `toml:"tailscale"`
	Complete  Complete           `toml:"complete"`
	Profiles  map[string]Profile `toml:"profiles"`

	// Root is the directory that holds the manifest.
	Root string `toml:"-"`
}

// Project names the repository and its state files.
type Project struct {
	Name     string `toml:"name"`
	EnvFile  string `toml:"env_file"`
	StateDir string `toml:"state_dir"`
}

// Port is a named port with no service behind it, such as the app server.
type Port struct {
	Default  int               `toml:"default"`
	Env      string            `toml:"env"`
	ExtraEnv map[string]string `toml:"extra_env"`
}

// PortCount is how many consecutive ports a service binds, TCP first.
type PortCount struct {
	TCP int `toml:"tcp"`
	UDP int `toml:"udp"`
}

// Service is a server devkit starts. A preset fills most fields; the
// manifest and the profile override them.
type Service struct {
	Preset        string             `toml:"preset"`
	DefaultPort   int                `toml:"default_port"`
	Ports         PortCount          `toml:"ports"`
	SkipIfSet     string             `toml:"skip_if_set"`
	Command       []string           `toml:"command"`
	Env           map[string]string  `toml:"env"`
	ReadyURL      string             `toml:"ready_url"`
	InstallHint   string             `toml:"install_hint"`
	DataDir       string             `toml:"data_dir"`
	Bucket        string             `toml:"bucket"`
	Buckets       []string           `toml:"buckets"`
	WebhookURL    *string            `toml:"webhook_url"`
	LoopbackOnly  *bool              `toml:"loopback_only"`
	ContainerName string             `toml:"container_name"`
	ExtraEnv      map[string]string  `toml:"extra_env"`
	Profiles      map[string]Service `toml:"profiles"`
}

// Database describes the per-worktree Postgres databases.
type Database struct {
	Env             string `toml:"env"`
	Pristine        string `toml:"pristine"`
	Worktree        string `toml:"worktree"`
	Build           string `toml:"build"`
	Migrate         string `toml:"migrate"`
	StorageDir      string `toml:"storage_dir"`
	PristineStorage string `toml:"pristine_storage"`
}

// Setup lists what `devkit setup` runs in a fresh worktree.
type Setup struct {
	Install     []string `toml:"install"`
	CopyEnvFile bool     `toml:"copy_env_file"`
	CopyEnvKeys []string `toml:"copy_env_keys"`
}

// Tailscale publishes one named port on the tailnet when Flag is "1".
type Tailscale struct {
	Flag      string `toml:"flag"`
	Publish   string `toml:"publish"`
	OriginEnv string `toml:"origin_env"`
}

// Complete tells `devkit complete` which processes belong to a checkout.
type Complete struct {
	PidFiles        []string `toml:"pid_files"`
	ProcessPatterns []string `toml:"process_patterns"`
}

// Profile overrides named ports, the database, and exported env for a run
// mode such as "acceptance".
type Profile struct {
	Ports    map[string]int    `toml:"ports"`
	Database string            `toml:"database"`
	ExtraEnv map[string]string `toml:"extra_env"`
}

// Find walks up from dir to the manifest file and returns its directory.
func Find(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s in this directory or any parent", FileName)
		}
		dir = parent
	}
}

// Load reads the manifest at root, applies defaults, and validates it.
func Load(root string) (*Manifest, error) {
	path := filepath.Join(root, FileName)
	var m Manifest
	meta, err := toml.DecodeFile(path, &m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, key := range undecoded {
			keys[i] = key.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	m.Root = root
	m.applyDefaults()
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

func (m *Manifest) applyDefaults() {
	if m.Project.EnvFile == "" {
		m.Project.EnvFile = ".env.development.local"
	}
	if m.Project.StateDir == "" {
		m.Project.StateDir = "tmp/devkit"
	}
}

func (m *Manifest) validate() error {
	if m.Project.Name == "" {
		return fmt.Errorf("project.name is required")
	}
	for name, port := range m.Ports {
		if port.Default == 0 {
			return fmt.Errorf("ports.%s.default is required", name)
		}
		if port.Env == "" {
			return fmt.Errorf("ports.%s.env is required", name)
		}
	}
	for name, service := range m.Services {
		if service.Preset == "" && len(service.Command) == 0 {
			return fmt.Errorf("services.%s needs a preset or a command", name)
		}
		if service.Preset != "" {
			if _, ok := presets[service.Preset]; !ok {
				return fmt.Errorf("services.%s: unknown preset %q (have %s)", name, service.Preset, strings.Join(PresetNames(), ", "))
			}
		}
		resolved := m.Resolve(name, "dev")
		if resolved.DefaultPort == 0 {
			return fmt.Errorf("services.%s.default_port is required", name)
		}
		if resolved.Ports.TCP == 0 && resolved.Ports.UDP == 0 {
			return fmt.Errorf("services.%s.ports is required", name)
		}
		if resolved.ReadyURL == "" {
			return fmt.Errorf("services.%s.ready_url is required", name)
		}
		if len(resolved.Command) == 0 {
			return fmt.Errorf("services.%s.command is required", name)
		}
	}
	if m.Database != nil {
		if m.Database.Env == "" || m.Database.Pristine == "" || m.Database.Worktree == "" {
			return fmt.Errorf("database needs env, pristine, and worktree")
		}
		if !strings.Contains(m.Database.Worktree, "{slug}") {
			return fmt.Errorf("database.worktree must contain {slug}")
		}
	}
	if m.Tailscale != nil {
		if _, ok := m.Ports[m.Tailscale.Publish]; !ok {
			return fmt.Errorf("tailscale.publish names no port in [ports]")
		}
	}
	return nil
}

// Resolve merges the preset, the manifest entry, and the profile for one
// service. The result has no Profiles.
func (m *Manifest) Resolve(name, profile string) Service {
	declared := m.Services[name]
	out := Service{}
	if declared.Preset != "" {
		out = merge(out, presets[declared.Preset])
	}
	out = merge(out, declared)
	if override, ok := declared.Profiles[profile]; ok {
		out = merge(out, override)
	}
	out.Profiles = nil
	return out
}

// ServiceNames returns the services in a stable order.
func (m *Manifest) ServiceNames() []string {
	names := make([]string, 0, len(m.Services))
	for name := range m.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PortNames returns the named ports in a stable order.
func (m *Manifest) PortNames() []string {
	names := make([]string, 0, len(m.Ports))
	for name := range m.Ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// StatePath returns an absolute path inside the state directory.
func (m *Manifest) StatePath(parts ...string) string {
	return filepath.Join(append([]string{m.Root, m.Project.StateDir}, parts...)...)
}

// EnvFilePath returns the absolute path of the env file in root.
func (m *Manifest) EnvFilePath(root string) string {
	return filepath.Join(root, m.Project.EnvFile)
}

func merge(base, over Service) Service {
	out := base
	if over.Preset != "" {
		out.Preset = over.Preset
	}
	if over.DefaultPort != 0 {
		out.DefaultPort = over.DefaultPort
	}
	if over.Ports.TCP != 0 || over.Ports.UDP != 0 {
		out.Ports = over.Ports
	}
	if over.SkipIfSet != "" {
		out.SkipIfSet = over.SkipIfSet
	}
	if len(over.Command) > 0 {
		out.Command = append([]string(nil), over.Command...)
	}
	out.Env = mergeMaps(base.Env, over.Env)
	if over.ReadyURL != "" {
		out.ReadyURL = over.ReadyURL
	}
	if over.InstallHint != "" {
		out.InstallHint = over.InstallHint
	}
	if over.DataDir != "" {
		out.DataDir = over.DataDir
	}
	if over.Bucket != "" {
		out.Bucket = over.Bucket
	}
	if len(over.Buckets) > 0 {
		out.Buckets = append([]string(nil), over.Buckets...)
	}
	if over.WebhookURL != nil {
		out.WebhookURL = over.WebhookURL
	}
	if over.LoopbackOnly != nil {
		out.LoopbackOnly = over.LoopbackOnly
	}
	if over.ContainerName != "" {
		out.ContainerName = over.ContainerName
	}
	out.ExtraEnv = mergeMaps(base.ExtraEnv, over.ExtraEnv)
	return out
}

func mergeMaps(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range over {
		out[key] = value
	}
	return out
}
