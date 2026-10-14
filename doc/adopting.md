# Adopting devkit in a repository

This guide takes a repository from nothing to a working `devkit.toml`, with
`just dev`, the test suites, CI, and linked worktrees all running through
devkit. Read it top to bottom the first time. The field reference at the end
is for later.

The dashboard (`dds/dashboard/app`) is the worked example. Its `devkit.toml`
uses every section below.

## What devkit does for a repository

- **Slots.** Each linked git worktree gets a slot number. The main checkout
  is slot 0. Slot N moves every port by N*10, so a worktree runs next to the
  main checkout with no port edits.
- **Services.** Local servers the app needs, such as an S3 (RustFS) and a
  LiveKit server, started per checkout for `just dev`, and per run for the
  test suites. Presets cover RustFS and LiveKit. Anything else needs a
  command, a port count, and a health URL.
- **Env.** One command prints every setting the app reads: ports, service
  endpoints and credentials, the database name, the Tailscale origin.
- **Database.** A pristine Postgres database built once by a hook you write,
  cloned per worktree with a file copy, dropped when the worktree is done.
- **Lifecycle.** `devkit setup` prepares a worktree. `devkit complete` tears
  it down: servers, Tailscale handlers, database, slot.

devkit does not run the app's own processes and does not replace foreman or
overmind. A Procfile line runs the services; the rest of the Procfile is
yours.

## Step 1: pin the tools

Add devkit and the server binaries to `mise.toml`. The versions below are
current at the time of writing.

```toml
[tools]
"github:gstark/devkit" = "0.1.2"
"github:rustfs/rustfs" = "1.0.1"
# LiveKit has no macOS release on GitHub: brew install livekit on a Mac.
"github:livekit/livekit" = { version = "1.13.9", os = ["linux"] }
```

Then `mise install`. Check with `devkit version`.

## Step 2: write `devkit.toml`

Put it at the repository root, next to `mise.toml`. Start from this skeleton
and delete what the repository does not need.

```toml
[project]
name = "my_app"                   # database prefix and lock names

[ports.web]
default = 3000
env = "MY_APP_PORT"               # the key your Procfile and scripts read

[services.s3]
preset = "rustfs"
default_port = 9000
skip_if_set = "AWS_ENDPOINT_URL_S3"
data_dir = "storage/rustfs"
bucket = "my-app-dev"
extra_env = { MY_APP_BUCKET = "{bucket}" }

[services.s3.profiles.test]
bucket = "my-app-test"

[database]
env = "MY_APP_DEV_DATABASE"       # the key config/database.yml reads
pristine = "my_app_worktree_pristine"
worktree = "my_app_wt_{slug}"
build = "script/dev/seed --yes"
migrate = "bundle exec rails db:migrate"

[setup]
install = ["bundle install --quiet", "npm ci"]

[complete]
pid_files = ["tmp/pids/server.pid"]
process_patterns = ["^puma[ :]", "rails server", "overmind", "devkit services up"]
```

Run `devkit env` from the repository root. It prints the exports for slot 0.
Fix every error it reports before going on. Unknown keys, missing required
fields, and bad template variables all fail here.

### Choosing named ports

A named port is one the app's own processes bind: the web server, a Vite dev
server, a worker's health port. devkit never starts anything on it. It only
picks the number and exports it. One entry per process, each with the env key
that process already reads, so nothing in the app changes.

### Choosing services

A service is a server devkit starts. Use a preset when one fits:

| Preset | Starts | Exports | After start |
|---|---|---|---|
| `rustfs` | `rustfs server --address 127.0.0.1:{port} {data_dir}` | `AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`; unsets `AWS_SESSION_TOKEN` | creates each bucket with a CORS rule for browser uploads |
| `livekit` | `livekit-server --config {config}` with a rendered `livekit.yaml` | `LIVEKIT_URL`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET`, `LIVEKIT_WEBHOOK_URL` when `webhook_url` is set | nothing |

Both presets use one fixed credential pair in every profile. They only ever
guard a server bound to loopback on this machine.

A generic service declares everything itself. Gotenberg through Docker:

```toml
[services.gotenberg]
default_port = 3005
ports = { tcp = 1 }
container_name = "my-app-gotenberg-{port}"
command = ["docker", "run", "--rm", "--name", "{container_name}", "--network", "host",
           "gotenberg/gotenberg:8", "gotenberg", "--api-port={port}"]
ready_url = "http://127.0.0.1:{port}/health"
install_hint = "Docker Desktop with host networking on"
extra_env = { GOTENBERG_BASE_URL = "http://127.0.0.1:{port}" }
```

`container_name` matters: devkit cannot signal a `docker run` process into
stopping its container, so `stop` runs `docker stop` on that name.

### Profiles

A profile is a run mode: `dev` (the default), `test`, `acceptance`, or any
name you pick. Two places take profile overrides.

A service may override any of its own fields:

```toml
[services.s3.profiles.test]
bucket = "my-app-test"
buckets = ["my-app-test", "my-app-test-import"]

[services.livekit.profiles.test]
webhook_url = ""          # empty string clears a value the dev profile set
loopback_only = true
extra_env = { LIVEKIT_CLIENT_URL = "" }   # empty string unsets the key
```

A top-level profile overrides named ports, the database, and adds env:

```toml
[profiles.acceptance]
ports = { web = 4710 }
database = "my_app_acceptance"

[profiles.acceptance.extra_env]
E2E_BASE_URL = "http://127.0.0.1:{ports.web}"
```

Profile ports follow the slot too. Slot 1 gets 4720, so two worktrees can run
acceptance at the same time.

A service's port in a non-dev profile is still its dev port unless the
profile overrides `default_port` or the run uses `--ephemeral`. A profile for
a run that happens next to `just dev` must do one or the other, or the two
servers fight over the port. Ephemeral is the simpler choice for test runs.

## Step 3: wire `just dev`

The dev script evaluates devkit's exports, then starts the process runner:

```sh
eval "$(devkit env --format sh)"
exec foreman start -f Procfile.dev      # or overmind
```

Explicit port arguments work by exporting the port key before the `eval`;
a value already in the shell wins over the slot.

`devkit env` also publishes the named port in `[tailscale]` when the flag is
set to `1` in the shell or the env file, and exports its origin. Pass
`--no-publish` from scripts that must not touch Tailscale.

Add one line to the Procfile:

```
services: devkit services up
```

`up` runs every enabled service in the foreground. A service whose
`skip_if_set` key points elsewhere, or whose binary is missing, prints why and
does not start. `up` keeps running either way, because foreman stops every
process the moment one exits.

## Step 4: write the database build hook

`devkit db pristine` runs `database.build` through `sh -c` from the repository
root with two extra variables:

- `DEVKIT_DATABASE`, the name to build into
- the key named in `database.env`, set to the same name

The hook must create the database itself. It usually is the repository's
existing seed script. Three rules:

1. **Respect the name devkit passes.** A seed script that also evaluates
   `devkit env` must capture the database variable first and restore it after,
   because the env file of a worktree carries that worktree's own database
   name. The dashboard's `scripts/dev/seed-dev-db` shows the pattern.
2. **Make it non-interactive.** Pass whatever flag skips its confirmation.
3. **Uploads go to the slot's S3.** If the seed uploads files, start the
   service with `devkit services start --only s3 --reuse --owner $$`. `--reuse`
   borrows the server of a running `just dev` and starts one otherwise; stop
   with `devkit services stop "$DEVKIT_PID_FILE"` on exit.

After the hook, devkit snapshots `database.storage_dir` into
`database.pristine_storage` when both are set. `devkit db clone` copies the
pristine database with `create database ... template ... strategy file_copy`,
clones the snapshot into the worktree's storage directory (APFS clones on
macOS, reflinks on Linux), writes the database name to the env file, and runs
`database.migrate` with the same two variables.

Postgres is reached through `psql` as the current user. Nothing else is
configurable yet.

## Step 5: the test suites

Each suite asks devkit for its own servers on random ports with empty data,
and stops them when the run ends. Apply both `env` and `unset` from the JSON.

Rails, before `config/environment` loads and before parallel workers fork:

```ruby
require "json"
output = IO.popen(["devkit", "services", "start", "--profile", "test", "--ephemeral",
                   "--owner", Process.pid.to_s, "--format", "json"], &:read)
abort "devkit services start failed" unless $?.success?
services = JSON.parse(output)
ENV.update(services.fetch("env"))
services.fetch("unset").each { |key| ENV.delete(key) }
owner = Process.pid
at_exit { system("devkit", "services", "stop", services.fetch("devkit_pid_file")) if Process.pid == owner }
```

Vitest, as a `globalSetup` of the project that needs the servers:

```ts
import { execFileSync } from 'node:child_process';
import type { TestProject } from 'vitest/node';

export async function setup(project: TestProject) {
  const services = JSON.parse(
    execFileSync('devkit', ['services', 'start', '--profile', 'test', '--ephemeral',
      '--owner', String(process.pid), '--format', 'json'], { encoding: 'utf8' })
  );
  Object.assign(process.env, services.env);
  for (const key of services.unset) delete process.env[key];
  project.provide('s3Endpoint', services.env.AWS_ENDPOINT_URL_S3);
  return () => execFileSync('devkit', ['services', 'stop', services.devkit_pid_file]);
}
```

Test helpers that used to own the servers become readers of `ENV`. Keep their
names so the tests do not change.

An acceptance script that runs next to `just dev` evaluates the acceptance
profile, then starts ephemeral servers:

```sh
eval "$(devkit env --profile acceptance --format sh --no-publish)"
devkit services stop --stale
eval "$(devkit services start --profile acceptance --ephemeral --owner $$ --format sh)"
trap 'devkit services stop "$DEVKIT_PID_FILE"' EXIT
```

## Step 6: CI

CI needs devkit and the server binaries on PATH at the pinned versions. Two
ways:

- `jdx/mise-action` with `install: true`, then add `mise bin-paths` to
  `GITHUB_PATH`. mise-action puts shims on PATH, and a shim resolves the tool
  from the working directory's `mise.toml`. Servers started from a temporary
  directory find none and exit at once.
- Install only the three tools, as the dashboard's `scripts/ci/install-dev-services`
  does, when the job brings its own Ruby or Node and should not install the
  whole `mise.toml`.

## Step 7: worktrees

The justfile recipes are one line each:

```
worktree-pristine *args:
    devkit db pristine {{ args }}

worktree *args: && dev
    devkit setup {{ args }}

worktree-complete:
    devkit complete
```

`devkit setup` refuses in the main checkout. In a linked worktree it runs the
`setup.install` commands, copies the env file or the listed keys from the main
checkout, claims a slot, clones the database, runs migrations. Two setups in
one worktree wait on each other, and slot claims and pristine builds across
worktrees share a lock in the common git directory.

`devkit complete` stops overmind if its socket exists, stops devkit's own
services, stops every process whose working directory is inside the checkout
and whose command line matches `complete.process_patterns` or whose pid is in
`complete.pid_files`, removes the Tailscale handlers for the slot's ports,
drops the worktree database, and clears the slot keys from the env file.

## Step 8: check it

```sh
devkit services doctor               # every binary on PATH
devkit env                           # the exports for this checkout
devkit services start --profile test --ephemeral --owner $$ --format json
devkit services stop tmp/devkit/test-$$.pids.json
```

Then a scratch worktree through `devkit setup` and `devkit complete`, a run of
each test suite, and a live `just dev`.

## Things that bit the first consumers

- **A preset export changed app behaviour.** The `rustfs` preset exports
  `AWS_REGION`. The dashboard reads its region from another key and uses
  `AWS_REGION` for the other AWS services. Its manifest unsets it with
  `AWS_REGION = ""` in `extra_env`. Check what each preset exports against
  what the app reads.
- **Tests hardcoded the old credentials.** The dashboard had `test_api_key`
  in three places. Read the constant from the helper instead.
- **Shells need `--owner $$`.** `$(devkit services start ...)` runs devkit in
  a subshell that exits at once. Without `--owner`, `--stale` treats the
  servers as orphaned. Ruby backticks and Node `execFileSync` are fine.
- **`DEVKIT_ENABLED_SERVICES`.** `devkit env` exports it. A later `devkit
  services up` under that env trusts it over `skip_if_set`, because devkit's
  own exports would otherwise make every service look externally configured.
  Do not strip it from the environment between `env` and `up`.
- **Apply `unset`.** The test profile clears keys such as `LIVEKIT_CLIENT_URL`
  through the `unset` list. A consumer that only applies `env` keeps the dev
  value.
- **The build hook runs in the worktree.** When `setup` has to build the
  pristine database, the hook runs from the worktree with the worktree's slot
  already claimed. The seed's uploads go to the worktree's RustFS port, and
  the storage snapshot carries them.

## Field reference

### `[project]`

| Key | Required | Default | Meaning |
|---|---|---|---|
| `name` | yes | | Prefix for lock names. Use the database prefix. |
| `env_file` | | `.env.development.local` | Where the slot, ports, and database name are written. |
| `state_dir` | | `tmp/devkit` | Pid files, rendered configs, server logs. |

### `[ports.<name>]`

| Key | Required | Meaning |
|---|---|---|
| `default` | yes | The slot 0 port. |
| `env` | yes | The key exported with the port. |
| `extra_env` | | More keys to export. Templates may use `{port}`. |

### `[services.<name>]`

| Key | Required | Preset fills | Meaning |
|---|---|---|---|
| `preset` | | | `rustfs` or `livekit`. |
| `default_port` | yes | | The slot 0 port. The first of the service's range. |
| `ports` | yes | yes | `{ tcp = N, udp = M }`: N TCP ports from `default_port`, then M UDP. |
| `command` | yes | yes | The binary and its arguments. Templates allowed. |
| `env` | | yes | Environment for the server process. |
| `ready_url` | yes | yes | Answers 2xx once the server is up. 15 second timeout. |
| `install_hint` | | yes | Shown when the binary is missing. |
| `skip_if_set` | | | An env key. When the shell has it, the service does not start. |
| `data_dir` | | | Data directory for the dev profile, relative to the root. Default `tmp/devkit/<name>-<profile>-data`. Ephemeral runs use a temporary directory. |
| `bucket` | | `devkit` | rustfs: the bucket to create. Available as `{bucket}`. |
| `buckets` | | | rustfs: several buckets. Overrides `bucket` for creation. |
| `webhook_url` | | | livekit: the webhook target. Empty string means none. |
| `loopback_only` | | | livekit: bind and offer 127.0.0.1 only. For test runs on one machine. |
| `container_name` | | | Docker services: the name `stop` passes to `docker stop`. |
| `extra_env` | | yes | Keys exported to the app. Empty string unsets the key. |
| `profiles.<profile>` | | | Any of the above, applied for that profile. |

Merge order: preset, then the manifest entry, then the profile. Maps merge
key by key. Lists and scalars replace.

### `[database]`

| Key | Required | Meaning |
|---|---|---|
| `env` | yes | The key the app reads the database name from. |
| `pristine` | yes | Name of the pristine database. |
| `worktree` | yes | Name template for a worktree database. Must contain `{slug}`. |
| `build` | | Shell command that creates the pristine database. Receives `DEVKIT_DATABASE` and `<env>`. |
| `migrate` | | Shell command run after a clone, with the same two variables. |
| `storage_dir` | | Directory snapshotted after a build and cloned after a clone. |
| `pristine_storage` | | Where the snapshot lives. Keep it outside every worktree. `{xdg_data}` works. |

`{slug}` is the worktree directory name in lower case, every character other
than a letter or digit replaced by `_`, cut to 40 characters.

### `[setup]`

| Key | Meaning |
|---|---|
| `install` | Shell commands run from the root before the database clone. |
| `copy_env_file` | Copy the main checkout's env file when the worktree has none. |
| `copy_env_keys` | Copy only these keys from the main checkout's env file when the worktree lacks them. |

### `[tailscale]`

| Key | Meaning |
|---|---|
| `flag` | Env key. When `1` in the shell or the env file, `devkit env` publishes. |
| `publish` | The named port to publish. |
| `origin_env` | Key exported with the published `https://` origin. |

### `[complete]`

| Key | Meaning |
|---|---|
| `pid_files` | Files holding a pid to stop, relative to the root. |
| `process_patterns` | Regular expressions matched against command lines. Only processes whose working directory is inside the checkout are stopped. |

### `[profiles.<profile>]`

| Key | Meaning |
|---|---|
| `ports` | Named port overrides, slot applied. |
| `database` | Database name for this profile. Wins over the env file. |
| `extra_env` | Keys to export. Applied after services, so it can override them. |

### Template variables

| Variable | Valid in | Value |
|---|---|---|
| `{port}` | a service, a named port's `extra_env` | that service's or port's number |
| `{port+N}` | same | the number plus N |
| `{ports.<name>}` | anywhere | a named port's number |
| `{origin.<name>}` | anywhere | the published origin of a named port, else `http://localhost:<port>` |
| `{bucket}` | a service | the service's `bucket` |
| `{data_dir}` | a service | the service's data directory |
| `{config}` | a service | the service's rendered config path |
| `{container_name}` | a service | the service's expanded container name |
| `{slug}` | anywhere | the worktree slug |
| `{root}` | anywhere | the repository root |
| `{xdg_data}` | anywhere | `$XDG_DATA_HOME`, else `~/.local/share` |

An unknown variable is an error.

### Env keys devkit exports

| Key | Value |
|---|---|
| `DEVKIT_SLOT` | the slot |
| `DEVKIT_PROFILE` | the profile |
| `DEVKIT_ENABLED_SERVICES` | comma-separated enabled services |
| `DEVKIT_PID_FILE` | from `services start`: the pid file to pass to `stop` |

### JSON output of `env` and `services start`

```json
{
  "profile": "test",
  "slot": 0,
  "ports": { "web": 3000 },
  "env": { "...": "..." },
  "unset": ["AWS_SESSION_TOKEN"],
  "services": { "s3": { "enabled": true, "port": 48868 } },
  "devkit_pid_file": "/repo/tmp/devkit/test-71578.pids.json"
}
```

`devkit_pid_file` is present only from `services start`.
