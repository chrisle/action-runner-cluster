# arc — GitHub Actions runner cluster orchestrator

Self-hosted runners for a **personal GitHub account** — no organization, no
GitHub Enterprise, no Kubernetes. Point arc at your account, run it on the
machines you already own, and jobs with `runs-on: [self-hosted, macos, arm64]`
start on your hardware seconds after they queue. Organizations work too, and
get a slightly simpler shape (see [Personal accounts vs
organizations](#personal-accounts-vs-organizations)).

Every runner is **ephemeral**: it takes exactly one job, unregisters itself, and
is deleted along with its entire filesystem. Nothing survives into the next job
— no caches, no checked-out repos, no toolchain downloads, no disk creeping
toward full.

```
$ arc status
account chrisle · 14 repos watched · updated 3s ago
github rate limit: 4821/5000 remaining, resets 41m12s

POOL     PROVIDER  MIN  MAX  LIVE  BUSY  IDLE  STARTING  QUEUED  DESIRED  STATE
macos    process   0    4    3     2     1     0         1       3        scaling up for queued jobs
linux    docker    1    8    2     1     1     0         0       2        at target
windows  process   0    2    0     0     0     0         0       0        at target
```

## Quick start

```sh
make build && make install     # or grab a binary from the releases page
arc config                     # account, token, and a pool for this machine
arc doctor                     # check credentials, scopes and providers
arc install                    # run it as a background service
```

`arc config` writes `~/.config/arc/arc.yaml`. Accepting the defaults gives this
machine one pool for its own platform, and on macOS or a container-less Windows
box the runner template downloads itself on first start — there is nothing else
to install.

Then add capacity by repeating those four commands on another machine. Hosts
never talk to each other; they coordinate through GitHub (see
[Several hosts, one account](#several-hosts-one-account)).

### Zero-config from 1Password

If a `github` item lives in a 1Password vault named `arc` (`credential` = the
token, `username` = the account), arc needs no config file at all:

```sh
arc install -max 4
```

It reads the vault once, caches the result at `~/.arc/credentials.json` so
restarts never prompt through the `op` CLI, decides on its own whether the login
is a user or an org, builds a pool for this machine's platform, and turns on
webhook-driven scaling. The cache is refreshed automatically if the token is
rotated or revoked.

Configuration is resolved in this order:

```
-config <path>
$ARC_CONFIG
./arc.yaml
~/.config/arc/arc.yaml            (written by "arc config")
1Password op://arc/github         (no file needed)
```

## Personal accounts vs organizations

GitHub has no user-level runners. An org registers runners once and every repo
in it can use them; a personal account can only register a runner **on a
specific repository**. arc handles that difference itself — it watches your
repos, and when a job queues it registers a runner against that job's repo and
starts it. What follows from the constraint:

| | Organization (`github.org`) | Personal account (`github.owner`) |
| --- | --- | --- |
| Runner registration | Once, org-wide | Per repo, per job |
| Token | Classic PAT with `admin:org`, or a GitHub App | Classic PAT with `repo` (fine-grained: Administration read/write) |
| GitHub App auth | Supported | Not supported — GitHub Apps cannot administer a user's repo runners |
| Runner groups | Optional (Enterprise Cloud / GHES) | Do not exist; leave `runner_group` empty |
| Warm runners (`min`) | Any value | Must be `0` |
| Webhooks | One org hook | One hook per watched repo |

`min` must be `0` on a personal account because a warm runner has to be
registered *somewhere*, and "somewhere" is one repo — it would sit idle there
while every other repo's jobs queued. Runners are created on demand instead,
which is why webhooks matter more here: a queued job pokes arc immediately
rather than waiting for the next poll.

Because a personal account can own hundreds of repos and each watched repo costs
API traffic, arc only watches repos pushed to in the last 30 days unless you
scope the set yourself with `github.repos`. Forks are skipped unless you set an
`include` list, since they rarely run their own Actions.

## The one thing to know about macOS

**Docker cannot run macOS runners.** Docker Desktop on a Mac runs Linux
containers inside a Linux VM — a "macOS container" would be a Linux runner that
cannot touch Xcode, code signing, or the iOS simulators. Apple does not permit
containerizing macOS, and no tool works around that.

So arc has two providers:

| Platform | Provider | What a runner is |
| -------- | --------- | --------------------------------------------------------------- |
| Linux | `docker` | A container, destroyed after one job |
| Windows | `docker` | A Windows container (needs Hyper-V, so Pro/Enterprise/Server, not Home) |
| Windows | `process` | A runner in a throwaway directory — the option for Windows Home |
| macOS | `process` | A runner in a private, copy-on-write directory, deleted after one job |

`arc doctor` rejects a macOS pool configured for Docker rather than letting you
find out from a runner that mysteriously has no Xcode.

## How the disposal guarantee works

Both providers give you the same promise by different means:

- **Docker** — the container is removed with its anonymous volumes (`v=true`),
  so the whole writable layer goes. Optionally `work_tmpfs: true` puts the job
  workspace in RAM, so it never reaches disk at all.
- **Process** — a pristine, never-configured runner installation is cloned per
  job. On macOS that uses APFS copy-on-write (`cp -Rc`), which is near-instant
  and costs almost no disk despite the runner being a few hundred megabytes;
  NTFS has no copy-on-write, so a Windows process pool pays a real ~250 MB copy
  per job. When the job finishes the entire clone is `rm -rf`'d.

The process provider's one honest limitation: anything a job writes *outside*
its instance directory — a real `~/Library` cache, `/tmp`, Homebrew — is beyond
its control. That is the unavoidable difference between a process and a
container. `RUNNER_TEMP` and `TMPDIR` are pointed inside the instance directory
to cover the common cases. There are also no CPU or memory limits, so a runaway
job can bog down the machine where a container would have been capped.

## Scaling

Each tick, for every pool:

```
desired = clamp(max(min, busy + queued), min, max)
```

`busy + queued` rather than just `queued` because runners are ephemeral: a
runner executing a job cannot also serve a queued one, so in-flight work and
waiting work both need capacity. `min` keeps warm runners so the first job of
the day doesn't pay full startup cost (org mode only).

Scale-down is conservative on purpose:

- Only **idle** runners are ever removed. A busy one is executing someone's
  build. GitHub itself refuses to deregister a busy runner (422), which is the
  backstop if the orchestrator's view is stale.
- A surplus runner must be continuously idle for `idle_timeout` before it goes,
  so a burst of jobs arriving seconds apart doesn't thrash create/destroy.
- Ephemeral runners exit on their own after one job anyway, so most scale-down
  is just arc reaping what already finished and topping back up to `min`.

### Which pool gets a job

A pool can serve a job when the pool's labels are a **superset** of the job's
`runs-on` labels — the same rule GitHub uses to route work. When several pools
qualify, the one with the fewest extra labels wins, so a plain
`runs-on: [self-hosted, linux]` job doesn't consume your scarce
`linux + gpu` machines. If the best-fit pool is at `max`, the job spills to
another eligible pool instead of waiting.

Jobs targeting GitHub-hosted images (`ubuntu-latest`, `macos-14`, …) are
ignored — GitHub runs those itself, and treating them as demand would spin up
local runners that never receive work.

### Several hosts, one account

Run arc on as many machines as you like against one account. Two Macs installed
with `-max 4` give the macOS labels a ceiling of 8. The hosts never talk to each
other and there is no leader: each derives a short id from its hostname, embeds
it in the runner names and webhook paths it owns, and only ever reaps or removes
its own.

They avoid double-provisioning by watching what the others have already done: a
queued job that an idle runner on another host can already serve is discounted
from this host's demand — repo-aware, so a runner registered on `app` only
covers `app`'s jobs. Worst case two hosts both create a runner for one job, one
takes it and the other goes idle and is culled at `idle_timeout`.

One arc can also drive remote Docker daemons instead (`host: ssh://…`), which is
the better shape when the remote machines are headless Linux boxes:

```
                      ┌──────────────────────────────┐
                      │  arc                         │
   GitHub API ◀──────▶│  polls + webhooks            │
                      │  mints JIT runner configs    │
                      └───┬───────────┬───────────┬──┘
        unix:// socket    │           │           │   ssh://
                          ▼           ▼           ▼
                   Linux docker   macOS process   Windows docker
                   (containers)   (cloned dirs)   (Windows containers)
```

Remote daemons are reached over `ssh://user@host`, which uses your existing SSH
keys and exposes no daemon port. `tcp://` with client certificates works too;
unauthenticated `tcp://` does not, deliberately.

## Webhooks

With `github.webhook: true` (the default in 1Password mode), arc reacts the
moment a job queues instead of waiting up to a poll interval:

- it starts a **Cloudflare quick tunnel** — no Cloudflare account, no inbound
  firewall rule, no port forwarding — and takes the random
  `https://<name>.trycloudflare.com` URL it is handed;
- it registers a `workflow_job` webhook pointing at that URL, on the org, or on
  every watched repo for a personal account;
- when the tunnel dies it restarts and re-registers, since the URL changes.

Polling drops to a 2-minute safety net for missed deliveries. Every failure in
this pipeline degrades to polling rather than to a stalled cluster, and
`arc uninstall` deletes the hooks this host registered.

## Setup detail

### Credentials

Personal account — a classic PAT with the **`repo`** scope:

```yaml
github:
  owner: your-username
  token: ${GITHUB_TOKEN}
```

Organization — a classic PAT with **`admin:org`**, or better for anything
long-lived, a GitHub App with **`organization_self_hosted_runners: write`**.
Installation tokens are minted hourly and scoped to the installation, and
there's no personal account whose departure breaks the cluster:

```yaml
github:
  org: your-org
  app:
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/arc/app-key.pem
```

Every `${VAR}` in the config comes from the environment. `~/.arc/env`,
`/etc/arc/env` and `%LOCALAPPDATA%\arc\op-token.txt` are sourced by the
installed service if present, which is where a host's token or 1Password
service-account token can live.

### Runner images and templates

Process pools need nothing: the runner template downloads itself into
`~/.arc/runner-template` on first start, staged and renamed into place so an
interrupted download can never masquerade as a working template.
`scripts/setup-macos-template.sh` (`make macos-template`) and
`scripts/setup-windows-template.ps1` do the same thing ahead of time if you'd
rather it not happen during the first job.

Docker pools need an image:

```sh
make image-linux REGISTRY=ghcr.io/you
make image-linux-multiarch REGISTRY=ghcr.io/you

# Windows — must run ON a Windows host with Docker Desktop in Windows-container mode
make image-windows REGISTRY=ghcr.io/you
```

The images and templates contain an **unconfigured** runner. arc pre-registers
each runner with GitHub and passes a just-in-time config at start, so one image
serves every pool and every project, and nothing in it is tied to an account.

### Running it

`arc run` runs in the foreground. `arc install` sets up the host's native
background service and starts it: a launchd **agent** on macOS (an agent, not a
daemon, or code signing fails), a systemd unit on Linux (`sudo arc install`, run
as the invoking user), a logon scheduled task on Windows. `arc start` /
`arc stop` drive it afterwards. Restarts never kill runners — they finish their
job and exit, and arc re-adopts what it finds.

Hand-written unit files are in `deploy/` if you want to manage the service
yourself.

### Removing a host

```sh
arc uninstall                  # -keep-config to leave a reinstall one command away
```

It stops and removes the service, destroys this host's runners and deregisters
them from GitHub, deletes this host's webhooks, and removes the files arc
created. It shows what it will do and asks first (`-yes` skips the prompt).

A runner executing a job stops it: uninstalling would fail a live build, so it
names the runners and exits without changing anything. `-force` overrides that.
Other arc hosts on the same account are never touched — runner names and webhook
paths carry a host id, and only this host's are removed.

Two things are deliberately left behind: `~/.arc/env`, `/etc/arc/env` and
`%LOCALAPPDATA%\arc\op-token.txt`, because you put those credentials there, and
the binary itself.

## Host tooling for process pools

Docker pools get their tools from the image. **Process pools get them from the
host**: each ephemeral runner is a clean clone of the runner template, but it
executes directly on the machine, so every CLI a workflow step calls must
already be installed there. `setup-*` actions that download their own toolchain
(`actions/setup-node`, `swatinem/rust-cache`) still work; anything a workflow
assumes was preinstalled on a GitHub-hosted image does not.

What the current fleet needed to take over a Tauri desktop-app release
workflow (build + sign + notarize on macOS, build + Azure Trusted Signing on
Windows, publish via `gh` on Linux) — a reasonable checklist for similar
workloads:

| Pool | Host-provided tools |
| --- | --- |
| linux | `gh`, `jq`, plus the runner system libs (`installdependencies.sh`) |
| macos | Xcode Command Line Tools, `rustup` |
| windows | VS Build Tools 2022 (MSVC), Windows SDK (`signtool`), `pwsh` 7, .NET 8 runtime, `rustup` |

Three gotchas:

- **PATH is captured when arc starts.** Runners inherit arc's environment, so a
  tool installed after arc came up (e.g. `rustup-init` appending to the Windows
  user PATH) is invisible to jobs until arc restarts. Restart when
  `arc status` shows no busy runners.
- **On Windows, `bash` must be Git Bash — not WSL.** Actions runs every
  `shell: bash` step, and the bash half of many composite actions
  (`dtolnay/rust-toolchain` among them), through whatever `bash` PATH resolves
  to first. If that is WSL's app-execution alias under `WindowsApps`, every one
  of those steps fails with a mangled path:

  ```
  /bin/bash: C:Userschris.arcinstances..._temp_xyz.sh: No such file or directory
  ```

  WSL bash cannot open a Windows path, and the backslashes vanish as escapes.
  GitHub-hosted runners ship Git Bash first on PATH; a workstation usually does
  not. Fix it for runners alone by prepending `C:\Program Files\Git\bin` in
  arc's launcher script (`%LOCALAPPDATA%\arc\run-arc.ps1`, rewritten by
  `arc install`) rather than to the user PATH, so interactive shells keep
  whatever `bash` their owner expects.
- Keep host tools out of the runner template itself — templates must stay
  unconfigured and generic.

## Adjusting min and max

Live, without a restart, persisted across restarts:

```sh
arc scale linux -max 16        # raise the ceiling for a big migration
arc scale linux -min 3         # keep more warm (org mode only)
arc scale linux -reset         # back to the config file's values

arc drain windows              # stop creating; running jobs finish untouched
arc resume windows
```

## Commands

| Command | What it does |
| --- | --- |
| `arc run [-max N]` | Start the orchestrator in the foreground |
| `arc config` | Create or edit the config with a wizard |
| `arc install [-max N]` | Install and start the background service |
| `arc start` / `arc stop` | Control the installed service |
| `arc uninstall [-yes] [-force] [-keep-config]` | Remove the service, this host's runners and arc's files |
| `arc status [-wide] [-json] [-watch 2s]` | Pools, runners, queued jobs |
| `arc scale <pool> [-min N] [-max N] [-reset]` | Change limits at runtime |
| `arc drain <pool>` / `arc resume <pool>` | Stop / restart runner creation |
| `arc logs <pool> <instance-id>` | Tail a runner's output |
| `arc doctor` | Check everything before it bites you |
| `arc update [-check] [-force]` | Replace this binary with the latest release |

`arc doctor` checks the config, the credentials and their scopes, repo
visibility, every Docker daemon, and every runner template — and reports all
failures at once rather than stopping at the first.

`GET /metrics` on the control API serves Prometheus gauges for live, busy, idle,
desired, queued and rate-limit remaining.

## API cost

There is no account-wide "list queued jobs" endpoint, so arc walks each repo's
active workflow runs. Every request is conditional on an ETag, and GitHub does
not charge a `304` against the rate limit — so quiet repos are free to poll.
Personal-account runner listings are per repo and conditional for the same
reason; without that they would eat the hourly budget on their own.

Narrow the work with `github.repos`:

```yaml
github:
  poll_interval: 15s
  repos:
    active_within: 720h        # skip repos with no push in 30 days
    include: ["service-*"]
    exclude: ["archived-*"]
```

`arc doctor` warns when your poll interval and repo count could outrun the
limit. Turning on webhooks is the bigger lever: polling then drops to a
2-minute safety net.

## Security notes

- The control API binds to loopback and changes scaling and reads runner logs.
  Set `server.token` if you must expose it, and put TLS in front of it.
- `docker_in_docker: true` gives every workflow root-equivalent control of that
  host's Docker daemon. It is off by default for that reason.
- Self-hosted runners and public repositories are a bad combination: anyone who
  can open a pull request can run code on your machine. The ephemeral model
  limits the blast radius but does not eliminate it, and on a personal account
  the machine is usually your own workstation. GitHub's guidance is to use
  self-hosted runners with private repos only.

## What's deliberately not here

- **Config hot-reload.** Limits change live with `arc scale`; anything else is a
  restart, which is cheap because runners are ephemeral and are left to finish.
- **Tart / VM runners.** The provider interface has room for it. macOS VMs would
  give per-job isolation the process provider can't, at a real cost in startup
  time and disk.
- **A coordination service.** Hosts sharing an account coordinate through
  GitHub's own state — runner names, hook paths, idle runners — so there is
  nothing extra to run, and nothing extra to lose.

## Development

```sh
make check          # fmt, vet, test
make test-unit      # skip the tests that need a Docker daemon
make crosscheck     # verify all five target platforms still compile
```

The Docker integration tests run against your local daemon, create real
containers under a test-only pool label, and clean up after themselves. They
skip automatically when no daemon is reachable.
