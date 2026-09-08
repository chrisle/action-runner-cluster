# arc — GitHub Actions runner cluster orchestrator

Self-hosted GitHub Actions runners for a **personal GitHub account** — no
organization, no GitHub Enterprise, no Kubernetes. Point arc at your account,
run it on the machines you already own, and jobs with
`runs-on: [self-hosted, macos, arm64]` start on your hardware seconds after they
queue. Organizations work too, and get a slightly simpler shape.

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

## Install

Grab a binary from the [releases
page](https://github.com/chrisle/action--runner-cluster/releases), or build it:

```sh
make build && make install     # single static binary, no runtime dependencies
```

## Run

```sh
arc config      # account, token, and a pool for this machine
arc doctor      # check credentials, scopes and providers
arc install     # run it as a background service
```

`arc config` writes `~/.config/arc/arc.yaml`. Accepting the defaults gives this
machine one pool for its own platform; on macOS, or on Windows without
containers, the runner template downloads itself on first start, so there is
nothing else to install. Docker pools need a runner image —
[Providers](docs/providers.md#runner-images-and-templates).

`arc run` runs it in the foreground instead. Add capacity by repeating the same
three commands on another machine: hosts never talk to each other, they
coordinate through GitHub.

Then check on it:

```sh
arc status              # -wide for individual runners, -watch 2s to refresh
arc scale macos -max 6  # change limits without a restart
arc uninstall           # remove this host cleanly
```

## Documentation

- [Personal accounts vs organizations](docs/personal-accounts.md) — what GitHub
  makes different, and what follows from it
- [Configuration](docs/configuration.md) — credentials, config discovery,
  1Password, repo filters, control API
- [Providers](docs/providers.md) — Docker vs process, why macOS can't be
  containerized, disposal, host tooling
- [Scaling](docs/scaling.md) — the scaling math, job-to-pool matching, several
  hosts on one account
- [Webhooks](docs/webhooks.md) — scaling the moment a job queues, over a
  Cloudflare quick tunnel
- [Operations](docs/operations.md) — every command, running as a service,
  updating, removing a host
- [Security notes](docs/security.md)
- [Development](docs/development.md)

`config.example.yaml` documents every configuration field with its rationale.
