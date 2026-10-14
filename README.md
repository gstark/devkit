# devkit

One binary that gives each git worktree of a project its own port slot, its own
local servers (S3 via RustFS, LiveKit, anything with a command and a health
URL), and its own Postgres database cloned from a pristine template. A
`devkit.toml` at the repo root declares what the project needs; `just` recipes,
Procfiles, and test runners call `devkit`.

To bring devkit into a repository, follow [doc/adopting.md](doc/adopting.md)
(`devkit docs adopting` prints the copy built into the installed binary):
the step-by-step, the `devkit.toml` field reference, and the gotchas the first
consumers hit. The design and the migration plan for those consumers are in
[doc/plan.md](doc/plan.md).

## Install

Pin it in the consumer repo's `mise.toml` next to the servers it runs:

```toml
[tools]
"github:gstark/devkit" = "0.1.0"
"github:rustfs/rustfs" = "1.0.1"
"github:livekit/livekit" = { version = "1.13.9", os = ["linux"] }  # brew install livekit on macOS
```

## Commands

| Command | Does |
|---|---|
| `devkit env [--profile P] [--format sh\|json]` | Print the env for this checkout: ports, service settings, database. `eval "$(devkit env)"` in `just dev`. |
| `devkit slot [claim\|release]` | Show, take, or give back this worktree's slot. Slot N moves every port by N*10. |
| `devkit services up` | Run the enabled services in the foreground. One Procfile line. |
| `devkit services start --profile P [--ephemeral] [--only s3] [--reuse] --owner $$` | Start them detached and print their env and pid file. Test runs use `--ephemeral`. `--reuse` leaves a server that already answers alone, for a one-off command next to a running `just dev`. |
| `devkit services stop PID_FILE \| --stale \| --all` | Stop them. |
| `devkit services doctor` | Check the binaries are installed. |
| `devkit db pristine [--yes]` | Rebuild the pristine database with the manifest's build hook. |
| `devkit db clone [--reset]` | Copy it into this worktree's database and run migrations. |
| `devkit db drop` | Drop this worktree's database. |
| `devkit setup [--reset]` | Install, env file, database, slot: everything a fresh worktree needs. |
| `devkit complete` | Stop this worktree's servers, remove Tailscale handlers, drop its database, release its slot. |
| `devkit tailscale publish\|off PORT_NAME` | Serve a named port on the tailnet, or stop. |
| `devkit docs adopting` | Print the adoption guide built into the binary. |

## Test adapters

Rails, before `config/environment` loads:

```ruby
services = JSON.parse(`devkit services start --profile test --ephemeral --format json`)
ENV.update(services.fetch("env"))
services.fetch("unset").each { |key| ENV.delete(key) }
owner = Process.pid
at_exit { system("devkit", "services", "stop", services.fetch("devkit_pid_file")) if Process.pid == owner }
```

Vitest global setup:

```ts
const services = JSON.parse(execFileSync('devkit', ['services', 'start', '--profile', 'test', '--ephemeral'], { encoding: 'utf8' }));
Object.assign(process.env, services.env);
return () => execFileSync('devkit', ['services', 'stop', services.devkit_pid_file]);
```

A shell script must pass `--owner $$`: `$(devkit ...)` runs devkit in a subshell
that exits at once, and `--stale` would otherwise treat the servers as orphaned.

## Development

```sh
go test ./...        # services tests skip without rustfs / livekit-server on PATH
                     # lifecycle tests skip without a local Postgres
mise install         # the two server binaries
goreleaser release --snapshot --clean   # local build of every platform
```
