package manifest

import "sort"

// Fixed credentials for the local servers. They only ever guard a server on
// this machine, bound to loopback.
const (
	RustfsAccessKey = "devkit"
	RustfsSecretKey = "devkit-local-secret"
	RustfsRegion    = "us-east-1"

	// LiveKit wants a secret of at least 32 characters.
	LivekitAPIKey    = "devkit"
	LivekitAPISecret = "devkit-local-livekit-secret-for-development"
)

// Preset names recognized in services.<name>.preset.
const (
	PresetRustfs  = "rustfs"
	PresetLivekit = "livekit"
)

var presets = map[string]Service{
	PresetRustfs: {
		Ports:       PortCount{TCP: 1},
		Command:     []string{"rustfs", "server", "--address", "127.0.0.1:{port}", "{data_dir}"},
		Env:         map[string]string{"RUSTFS_ACCESS_KEY": RustfsAccessKey, "RUSTFS_SECRET_KEY": RustfsSecretKey},
		ReadyURL:    "http://127.0.0.1:{port}/health/ready",
		InstallHint: "`mise install` (pin github:rustfs/rustfs in mise.toml)",
		Bucket:      "devkit",
		ExtraEnv: map[string]string{
			"AWS_ENDPOINT_URL_S3":   "http://127.0.0.1:{port}",
			"AWS_ACCESS_KEY_ID":     RustfsAccessKey,
			"AWS_SECRET_ACCESS_KEY": RustfsSecretKey,
			"AWS_REGION":            RustfsRegion,
			// Any session token in the shell would be sent with the fixed keys.
			"AWS_SESSION_TOKEN": "",
		},
	},
	PresetLivekit: {
		Ports:       PortCount{TCP: 2, UDP: 1},
		Command:     []string{"livekit-server", "--config", "{config}"},
		ReadyURL:    "http://127.0.0.1:{port}/",
		InstallHint: "`brew install livekit` on macOS, `mise install` on Linux (pin github:livekit/livekit in mise.toml)",
		ExtraEnv: map[string]string{
			"LIVEKIT_URL":        "ws://127.0.0.1:{port}",
			"LIVEKIT_API_KEY":    LivekitAPIKey,
			"LIVEKIT_API_SECRET": LivekitAPISecret,
		},
	},
}

// PresetNames lists the built-in presets.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
