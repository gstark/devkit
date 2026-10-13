// Package cmd defines the devkit command line.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gstark/devkit/internal/envfile"
	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/resolve"
	"github.com/gstark/devkit/internal/slot"
	"github.com/gstark/devkit/internal/tailscale"
)

// Version is set by the release build.
var Version = "dev"

// Execute runs the command line.
func Execute() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "devkit:", err)
		os.Exit(1)
	}
}

// app is what every command loads first.
type app struct {
	Manifest *manifest.Manifest
	Worktree gitwt.Info
	Out      io.Writer // progress; stderr when stdout carries data
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "devkit",
		Short:         "Port slots, local services, and databases for git worktrees",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersion(), newSlot(), newEnv(), newServices(), newDB(), newSetup(), newComplete(), newTailscale())
	return root
}

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the devkit version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), Version)
			return nil
		},
	}
}

func load(out io.Writer) (*app, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := manifest.Find(cwd)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(root)
	if err != nil {
		return nil, err
	}
	wt, err := gitwt.Discover(root)
	if err != nil {
		return nil, err
	}
	return &app{Manifest: m, Worktree: wt, Out: out}, nil
}

// currentSlot picks the slot: the flag, then DEVKIT_SLOT in the shell, then
// the env file. In a worktree with no slot, lenient returns 0 instead of
// an error.
func (a *app) currentSlot(flag int, lenient bool) (int, error) {
	if flag >= 0 {
		return flag, nil
	}
	if raw := os.Getenv(resolve.SlotKey); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			return value, nil
		}
	}
	current, err := slot.Current(a.Manifest, a.Worktree)
	if errors.Is(err, slot.ErrUnclaimed) && lenient {
		return 0, nil
	}
	return current, err
}

// plan resolves the manifest for this checkout.
func (a *app) plan(profile string, slotFlag int, ephemeral, publish bool) (*resolve.Plan, error) {
	current, err := a.currentSlot(slotFlag, ephemeral)
	if err != nil {
		return nil, err
	}
	values, err := envfile.ReadAll(a.Manifest.EnvFilePath(a.Worktree.Root))
	if err != nil {
		return nil, err
	}
	shell := resolve.EnvironMap()
	origins := map[string]string{}
	if publish && a.Manifest.Tailscale != nil {
		ts := a.Manifest.Tailscale
		if shell[ts.Flag] == "1" || (shell[ts.Flag] == "" && values[ts.Flag] == "1") {
			port := resolve.Options{Manifest: a.Manifest, Slot: current, ShellEnv: shell, EnvFile: values}
			plain, err := resolve.Resolve(port)
			if err != nil {
				return nil, err
			}
			url, err := tailscale.Publish(plain.Ports[ts.Publish])
			if err != nil {
				fmt.Fprintf(a.Out, "Could not publish port %d with tailscale serve (%v). Is Tailscale up, and are you its operator?\n", plain.Ports[ts.Publish], err)
			} else {
				origins[ts.Publish] = url
			}
		}
	}
	return resolve.Resolve(resolve.Options{
		Manifest:  a.Manifest,
		Worktree:  a.Worktree,
		Profile:   profile,
		Slot:      current,
		Ephemeral: ephemeral,
		ShellEnv:  shell,
		EnvFile:   values,
		Origins:   origins,
	})
}

// writeEnv prints a plan's env as shell exports or JSON.
func writeEnv(w io.Writer, plan *resolve.Plan, format string, extra map[string]string) error {
	switch format {
	case "sh":
		keys := make([]string, 0, len(plan.Env)+len(extra))
		for key := range plan.Env {
			keys = append(keys, key)
		}
		for key := range extra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := extra[key]
			if !ok {
				value = plan.Env[key]
			}
			fmt.Fprintf(w, "export %s=%s\n", key, shellQuote(value))
		}
		for _, key := range plan.Unset {
			fmt.Fprintf(w, "unset %s\n", key)
		}
		return nil
	case "json":
		payload := map[string]any{
			"profile": plan.Profile,
			"slot":    plan.Slot,
			"ports":   plan.Ports,
			"env":     plan.Env,
			"unset":   plan.Unset,
		}
		services := map[string]any{}
		for _, service := range plan.Services {
			entry := map[string]any{"enabled": service.Enabled, "port": service.Port}
			if !service.Enabled {
				entry["skip_reason"] = service.SkipReason
			}
			services[service.Name] = entry
		}
		payload["services"] = services
		for key, value := range extra {
			payload[strings.ToLower(key)] = value
		}
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	default:
		return fmt.Errorf("unknown format %q (sh or json)", format)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
