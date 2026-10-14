// Package resolve turns a manifest, a profile, and a slot into concrete
// ports, commands, and the env the app reads.
package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/ports"
)

// Env keys devkit itself exports.
const (
	SlotKey            = "DEVKIT_SLOT"
	ProfileKey         = "DEVKIT_PROFILE"
	EnabledServicesKey = "DEVKIT_ENABLED_SERVICES"
	PidFileKey         = "DEVKIT_PID_FILE"
)

// Options is the input to Resolve.
type Options struct {
	Manifest *manifest.Manifest
	Worktree gitwt.Info
	Profile  string
	Slot     int
	// Ephemeral picks random free ports and temporary data directories, for a
	// test run.
	Ephemeral bool
	// ShellEnv is the caller's environment.
	ShellEnv map[string]string
	// EnvFile holds the values of the checkout's env file.
	EnvFile map[string]string
	// Origins maps a named port to its published origin, such as a Tailscale
	// URL. A port with no entry gets http://localhost:<port>.
	Origins map[string]string
}

// Plan is the resolved result.
type Plan struct {
	Profile  string
	Slot     int
	Ports    map[string]int
	Services []*Service
	// Env is every key the app should see, and Unset every key it should not.
	Env      map[string]string
	Unset    []string
	Database string
}

// Service is one resolved service.
type Service struct {
	Name          string
	Spec          manifest.Service
	Enabled       bool
	SkipReason    string
	Port          int
	DataDir       string
	ConfigPath    string
	Command       []string
	ProcEnv       map[string]string
	ReadyURL      string
	ContainerName string
	Buckets       []string
	PublicRead    bool
	WebhookURL    string
	Env           map[string]string
	Unset         []string
	Ephemeral     bool
}

// Enabled returns the services that will run.
func (p *Plan) Enabled() []*Service {
	var out []*Service
	for _, service := range p.Services {
		if service.Enabled {
			out = append(out, service)
		}
	}
	return out
}

// Resolve computes the plan. With Ephemeral it creates the temporary data
// directories, because the commands name them.
func Resolve(o Options) (*Plan, error) {
	m := o.Manifest
	if o.Profile == "" {
		o.Profile = "dev"
	}
	profile := m.Profiles[o.Profile]
	plan := &Plan{Profile: o.Profile, Slot: o.Slot, Ports: map[string]int{}, Env: map[string]string{}}

	for _, name := range m.PortNames() {
		plan.Ports[name] = o.namedPort(name, profile)
	}

	vars := o.baseVars(plan)

	for _, name := range m.PortNames() {
		port := m.Ports[name]
		plan.Env[port.Env] = strconv.Itoa(plan.Ports[name])
		extra, err := vars.With("port", strconv.Itoa(plan.Ports[name])).ExpandMap(port.ExtraEnv)
		if err != nil {
			return nil, fmt.Errorf("ports.%s.extra_env: %w", name, err)
		}
		plan.addEnv(extra)
	}

	// Every service's port first, so one service can name another's, such as
	// Gotenberg's allow-list naming the S3 port.
	servicePorts := map[string]int{}
	for _, name := range m.ServiceNames() {
		spec := m.Resolve(name, o.Profile)
		if o.Ephemeral {
			port, err := ports.FreeBase(spec.Ports.TCP, spec.Ports.UDP)
			if err != nil {
				return nil, fmt.Errorf("services.%s: %w", name, err)
			}
			servicePorts[name] = port
		} else {
			servicePorts[name] = ports.ForSlot(spec.DefaultPort, o.Slot)
		}
		vars["services."+name+".port"] = strconv.Itoa(servicePorts[name])
	}

	for _, name := range m.ServiceNames() {
		service, err := o.service(name, servicePorts[name], vars)
		if err != nil {
			return nil, err
		}
		plan.Services = append(plan.Services, service)
		if service.Enabled {
			plan.addEnvAndUnset(service.Env, service.Unset)
		}
	}

	plan.Database = o.database(profile)
	if plan.Database != "" {
		plan.Env[m.Database.Env] = plan.Database
	}

	extra, err := vars.ExpandMap(profile.ExtraEnv)
	if err != nil {
		return nil, fmt.Errorf("profiles.%s.extra_env: %w", o.Profile, err)
	}
	plan.addEnv(extra)

	if m.Tailscale != nil {
		if origin, ok := o.Origins[m.Tailscale.Publish]; ok && m.Tailscale.OriginEnv != "" {
			plan.Env[m.Tailscale.OriginEnv] = origin
		}
	}

	var enabled []string
	for _, service := range plan.Enabled() {
		enabled = append(enabled, service.Name)
	}
	plan.Env[SlotKey] = strconv.Itoa(o.Slot)
	plan.Env[ProfileKey] = o.Profile
	plan.Env[EnabledServicesKey] = strings.Join(enabled, ",")
	sort.Strings(plan.Unset)
	return plan, nil
}

func (o Options) namedPort(name string, profile manifest.Profile) int {
	port := o.Manifest.Ports[name]
	if override, ok := profile.Ports[name]; ok {
		return ports.ForSlot(override, o.Slot)
	}
	if value, ok := numeric(o.ShellEnv[port.Env]); ok {
		return value
	}
	if value, ok := numeric(o.EnvFile[port.Env]); ok {
		return value
	}
	return ports.ForSlot(port.Default, o.Slot)
}

func (o Options) database(profile manifest.Profile) string {
	m := o.Manifest
	if m.Database == nil {
		return ""
	}
	if profile.Database != "" {
		return profile.Database
	}
	if value := o.EnvFile[m.Database.Env]; value != "" {
		return value
	}
	return o.ShellEnv[m.Database.Env]
}

func (o Options) baseVars(plan *Plan) Vars {
	vars := Vars{
		"root":     o.Manifest.Root,
		"slug":     o.Worktree.Slug(),
		"xdg_data": xdgData(),
	}
	for name, port := range plan.Ports {
		vars["ports."+name] = strconv.Itoa(port)
		if origin, ok := o.Origins[name]; ok {
			vars["origin."+name] = origin
		} else {
			vars["origin."+name] = fmt.Sprintf("http://localhost:%d", port)
		}
	}
	if o.Manifest.Database != nil {
		vars["database"] = o.database(o.Manifest.Profiles[o.Profile])
	}
	return vars
}

func (o Options) service(name string, port int, base Vars) (*Service, error) {
	m := o.Manifest
	spec := m.Resolve(name, o.Profile)
	service := &Service{Name: name, Spec: spec, Enabled: true, Ephemeral: o.Ephemeral, Port: port}
	service.PublicRead = spec.PublicRead != nil && *spec.PublicRead

	if listed, ok := o.ShellEnv[EnabledServicesKey]; ok {
		service.Enabled = contains(strings.Split(listed, ","), name)
		if !service.Enabled {
			service.SkipReason = fmt.Sprintf("not in %s", EnabledServicesKey)
		}
	} else if spec.SkipIfSet != "" {
		if value := o.ShellEnv[spec.SkipIfSet]; value != "" {
			service.Enabled = false
			service.SkipReason = fmt.Sprintf("%s is set to %s", spec.SkipIfSet, value)
		}
	}

	if o.Ephemeral {
		if service.Enabled {
			dir, err := os.MkdirTemp("", "devkit-"+name+"-")
			if err != nil {
				return nil, err
			}
			service.DataDir = dir
			service.ConfigPath = filepath.Join(dir, name+".yaml")
		}
	} else {
		if spec.DataDir != "" {
			service.DataDir = absolute(m.Root, spec.DataDir)
		} else {
			service.DataDir = m.StatePath(name + "-" + o.Profile + "-data")
		}
		service.ConfigPath = m.StatePath(name + "-" + o.Profile + ".yaml")
	}

	vars := base.With("port", strconv.Itoa(service.Port)).
		With("data_dir", service.DataDir).
		With("config", service.ConfigPath).
		With("bucket", spec.Bucket)

	var err error
	// The container name first, so the command can name it.
	if service.ContainerName, err = vars.Expand(spec.ContainerName); err != nil {
		return nil, fmt.Errorf("services.%s.container_name: %w", name, err)
	}
	vars = vars.With("container_name", service.ContainerName)
	if service.Command, err = vars.ExpandAll(spec.Command); err != nil {
		return nil, fmt.Errorf("services.%s.command: %w", name, err)
	}
	if service.ProcEnv, err = vars.ExpandMap(spec.Env); err != nil {
		return nil, fmt.Errorf("services.%s.env: %w", name, err)
	}
	if service.ReadyURL, err = vars.Expand(spec.ReadyURL); err != nil {
		return nil, fmt.Errorf("services.%s.ready_url: %w", name, err)
	}
	if spec.WebhookURL != nil {
		if service.WebhookURL, err = vars.Expand(*spec.WebhookURL); err != nil {
			return nil, fmt.Errorf("services.%s.webhook_url: %w", name, err)
		}
	}
	service.Buckets = spec.Buckets
	if len(service.Buckets) == 0 && spec.Bucket != "" {
		service.Buckets = []string{spec.Bucket}
	}

	exported, err := vars.ExpandMap(spec.ExtraEnv)
	if err != nil {
		return nil, fmt.Errorf("services.%s.extra_env: %w", name, err)
	}
	if spec.Preset == manifest.PresetLivekit {
		exported["LIVEKIT_WEBHOOK_URL"] = service.WebhookURL
	}
	service.Env = map[string]string{}
	for key, value := range exported {
		if value == "" {
			service.Unset = append(service.Unset, key)
		} else {
			service.Env[key] = value
		}
	}
	sort.Strings(service.Unset)
	return service, nil
}

func (p *Plan) addEnv(values map[string]string) {
	p.addEnvAndUnset(values, nil)
}

func (p *Plan) addEnvAndUnset(values map[string]string, unset []string) {
	for key, value := range values {
		if value == "" {
			p.Unset = appendUnique(p.Unset, key)
			delete(p.Env, key)
			continue
		}
		p.Env[key] = value
	}
	for _, key := range unset {
		p.Unset = appendUnique(p.Unset, key)
		delete(p.Env, key)
	}
}

// Vars wraps manifest.Vars with a copy-on-add helper.
type Vars = manifest.Vars

func numeric(s string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(s))
	return value, err == nil && value > 0
}

func absolute(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func xdgData() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}
	return append(values, value)
}

// EnvironMap turns os.Environ() into a map.
func EnvironMap() map[string]string {
	out := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, _ := strings.Cut(pair, "=")
		out[key] = value
	}
	return out
}
