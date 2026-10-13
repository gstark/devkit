package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/gstark/devkit/internal/lifecycle"
	"github.com/gstark/devkit/internal/proc"
	"github.com/gstark/devkit/internal/resolve"
	"github.com/gstark/devkit/internal/services"
	"github.com/gstark/devkit/internal/slot"
	"github.com/gstark/devkit/internal/tailscale"
)

func newSlot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slot",
		Short: "Print this checkout's slot (0 for the main checkout)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			current, err := slot.Current(a.Manifest, a.Worktree)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), current)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "claim",
		Short: "Give this linked worktree the first free slot and write its ports to the env file",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			claimed, err := slot.Claim(a.Manifest, a.Worktree, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), claimed)
			return nil
		},
	}, &cobra.Command{
		Use:   "release",
		Short: "Remove the slot and port keys from the env file",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return slot.Release(a.Manifest, a.Worktree)
		},
	})
	return cmd
}

func newEnv() *cobra.Command {
	var profile, format string
	var slotFlag int
	var noPublish bool
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print the env devkit exports for this checkout: ports, services, database",
		Long: `Print the env devkit exports for this checkout. In a shell:

  eval "$(devkit env --format sh)"

With the manifest's tailscale flag set to 1 in the shell or the env file,
env also publishes the named port with tailscale serve and exports its origin.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			plan, err := a.plan(profile, slotFlag, false, !noPublish)
			if err != nil {
				return err
			}
			return writeEnv(cmd.OutOrStdout(), plan, format, nil)
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "dev", "profile: dev, test, acceptance, or any the manifest names")
	cmd.Flags().StringVar(&format, "format", "sh", "output format: sh or json")
	cmd.Flags().IntVar(&slotFlag, "slot", -1, "use this slot instead of the checkout's")
	cmd.Flags().BoolVar(&noPublish, "no-publish", false, "never run tailscale serve, even when the flag is set")
	return cmd
}

func newServices() *cobra.Command {
	cmd := &cobra.Command{Use: "services", Short: "Run the servers the manifest declares"}

	var upProfile string
	up := &cobra.Command{
		Use:   "up",
		Short: "Run every enabled service in the foreground, for a Procfile line",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			plan, err := a.plan(upProfile, -1, false, false)
			if err != nil {
				return err
			}
			return services.Up(context.Background(), plan, a.Manifest.Root, cmd.ErrOrStderr())
		},
	}
	up.Flags().StringVar(&upProfile, "profile", "dev", "profile to run")

	var startProfile, startFormat string
	var ephemeral, reuse bool
	var owner int
	var only []string
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the services detached and print their env; stop them with `devkit services stop`",
		Long: `Start the services detached and print their env. The servers outlive devkit
and belong to the calling process (the pid file records it), so a test run
can start them before it boots and stop them when it ends:

  eval "$(devkit services start --profile acceptance --format sh --owner $$)"
  ...
  devkit services stop "$DEVKIT_PID_FILE"

With --ephemeral the services take random free ports and temporary data
directories, which stop removes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			plan, err := a.plan(startProfile, -1, ephemeral, false)
			if err != nil {
				return err
			}
			ownerPID := owner
			if ownerPID <= 0 {
				ownerPID = os.Getppid()
			}
			pidPath := a.Manifest.StatePath(fmt.Sprintf("%s-%d.pids.json", plan.Profile, ownerPID))
			pidFile, err := services.Start(context.Background(), plan, a.Manifest.Root, pidPath, services.StartOptions{Owner: owner, Reuse: reuse, Only: only}, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return writeEnv(cmd.OutOrStdout(), plan, startFormat, map[string]string{resolve.PidFileKey: pidFile.Path})
		},
	}
	start.Flags().StringVar(&startProfile, "profile", "dev", "profile to run")
	start.Flags().StringVar(&startFormat, "format", "json", "output format: json or sh")
	start.Flags().BoolVar(&ephemeral, "ephemeral", false, "random free ports and temporary data directories")
	start.Flags().BoolVar(&reuse, "reuse", false, "skip a service that already answers on its port, and leave it running on stop")
	start.Flags().StringSliceVar(&only, "only", nil, "start only these services (comma separated)")
	start.Flags().IntVar(&owner, "owner", 0, "pid the servers belong to, for --stale (default: the parent process; pass $$ from a shell script)")

	var stale, all bool
	stop := &cobra.Command{
		Use:   "stop [PID_FILE]",
		Short: "Stop the services in a pid file, or every stale one (--stale), or every one (--all)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			switch {
			case all:
				return services.StopAll(a.Manifest.StatePath(), out)
			case stale:
				count, err := services.StopStale(a.Manifest.StatePath(), out)
				if err != nil {
					return err
				}
				if count == 0 {
					fmt.Fprintln(out, "No stale services.")
				}
				return nil
			case len(args) == 1:
				pidFile, err := proc.LoadPidFile(args[0])
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				return services.Stop(pidFile, out)
			default:
				return errors.New("give a pid file, --stale, or --all")
			}
		},
	}
	stop.Flags().BoolVar(&stale, "stale", false, "stop services whose starting process is gone")
	stop.Flags().BoolVar(&all, "all", false, "stop every devkit service of this checkout")

	var doctorProfile string
	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check that each service's binary is installed",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := load(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			plan, err := a.plan(doctorProfile, -1, false, false)
			if err != nil {
				return err
			}
			return services.Doctor(plan, cmd.OutOrStdout())
		},
	}
	doctor.Flags().StringVar(&doctorProfile, "profile", "dev", "profile to check")

	cmd.AddCommand(up, start, stop, doctor)
	return cmd
}

func newDB() *cobra.Command {
	cmd := &cobra.Command{Use: "db", Short: "Pristine and per-worktree Postgres databases"}

	var yes bool
	pristine := &cobra.Command{
		Use:   "pristine",
		Short: "Rebuild the pristine database with the manifest's build hook and snapshot the storage files",
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := loadLifecycle(cmd)
			if err != nil {
				return err
			}
			return lc.Pristine(yes)
		},
	}
	pristine.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")

	var reset bool
	clone := &cobra.Command{
		Use:   "clone",
		Short: "Create this worktree's database from the pristine one and run migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := loadLifecycle(cmd)
			if err != nil {
				return err
			}
			return lc.Clone(reset)
		},
	}
	clone.Flags().BoolVar(&reset, "reset", false, "copy the pristine database again")

	drop := &cobra.Command{
		Use:   "drop",
		Short: "Drop this worktree's database, and no other",
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := loadLifecycle(cmd)
			if err != nil {
				return err
			}
			return lc.Drop()
		},
	}

	cmd.AddCommand(pristine, clone, drop)
	return cmd
}

func newSetup() *cobra.Command {
	var reset bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Prepare a linked worktree: install, env file, database, slot",
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := loadLifecycle(cmd)
			if err != nil {
				return err
			}
			return lc.Setup(reset)
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false, "copy the pristine database again")
	return cmd
}

func newComplete() *cobra.Command {
	return &cobra.Command{
		Use:   "complete",
		Short: "Finish with a linked worktree: stop its servers, remove Tailscale handlers, drop its database, release its slot",
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := loadLifecycle(cmd)
			if err != nil {
				return err
			}
			return lc.Complete()
		},
	}
}

func newTailscale() *cobra.Command {
	cmd := &cobra.Command{Use: "tailscale", Short: "Publish a named port on the tailnet, or remove it"}
	cmd.AddCommand(&cobra.Command{
		Use:   "publish PORT_NAME",
		Short: "Serve the named port at https://<MagicDNS name>:<port> and print the URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, port, err := namedPort(cmd, args[0])
			if err != nil {
				return err
			}
			_ = a
			url, err := tailscale.Publish(port)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), url)
			return nil
		},
	}, &cobra.Command{
		Use:   "off PORT_NAME",
		Short: "Remove the serve handlers that proxy to the named port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, port, err := namedPort(cmd, args[0])
			if err != nil {
				return err
			}
			return tailscale.Off(port, cmd.ErrOrStderr())
		},
	})
	return cmd
}

func namedPort(cmd *cobra.Command, name string) (*app, int, error) {
	a, err := load(cmd.ErrOrStderr())
	if err != nil {
		return nil, 0, err
	}
	plan, err := a.plan("dev", -1, false, false)
	if err != nil {
		return nil, 0, err
	}
	port, ok := plan.Ports[name]
	if !ok {
		return nil, 0, fmt.Errorf("no port named %q in the manifest (have %v)", name, a.Manifest.PortNames())
	}
	return a, port, nil
}

func loadLifecycle(cmd *cobra.Command) (lifecycle.Context, error) {
	a, err := load(cmd.ErrOrStderr())
	if err != nil {
		return lifecycle.Context{}, err
	}
	return lifecycle.Context{Manifest: a.Manifest, Worktree: a.Worktree, Out: cmd.OutOrStdout(), Stdin: os.Stdin}, nil
}

var _ = strconv.Itoa
