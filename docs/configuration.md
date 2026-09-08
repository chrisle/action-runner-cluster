# Configuration

`arc config` walks the whole file interactively — creating it from scratch, or
editing an existing one with every prompt prefilled, so Enter-Enter-Enter is a
no-op. `config.example.yaml` documents every field with its rationale.

## Where configuration comes from

Resolved in this order, first hit wins:

```
-config <path>
$ARC_CONFIG
./arc.yaml
~/.config/arc/arc.yaml            (written by "arc config")
1Password op://arc/github         (no file needed)
```

Every `${VAR}` in the file is expanded from the environment; `${VAR:-default}`
makes one optional. A `${VAR}` with no value and no default is an error rather
than an empty string, so a blanked token fails at load instead of as a confusing
401 much later.

`~/.arc/env`, `/etc/arc/env` and `%LOCALAPPDATA%\arc\op-token.txt` are sourced
by the installed service if present. That is where a host's GitHub token or
1Password service-account token belongs — `arc uninstall` deliberately leaves
them alone.

## Credentials

Personal account — a classic PAT with the **`repo`** scope:

```yaml
github:
  owner: your-username
  token: ${GITHUB_TOKEN}
```

Organization — a classic PAT with **`admin:org`**:

```yaml
github:
  org: your-org
  token: ${GITHUB_TOKEN}
```

For anything long-lived on an org, a GitHub App with
**`organization_self_hosted_runners: write`** is better: installation tokens are
minted hourly and scoped to the installation, and there's no personal account
whose departure breaks the cluster.

```yaml
github:
  org: your-org
  app:
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/arc/app-key.pem
```

App auth does not work for a personal account — see
[Personal accounts vs organizations](personal-accounts.md).

## Zero-config from 1Password

If a `github` item lives in a 1Password vault named `arc` (`credential` = the
token, `username` = the account), arc needs no config file at all:

```sh
arc install -max 4
```

It reads the vault once, caches the result at `~/.arc/credentials.json` so
restarts never prompt through the `op` CLI, decides for itself whether the login
is a user or an org, builds a pool for this machine's platform, and turns on
webhook-driven scaling. The cache is refreshed automatically when the token is
rotated or revoked; if GitHub is unreachable but the cache was validated before,
arc comes up anyway rather than being a service that cannot boot without
network.

Set the item up with:

```sh
op vault create arc
op item create --vault arc --category "API Credential" --title github \
  credential=<token> username=<account>
```

Authentication is the `op` CLI's problem: the desktop app integration (biometric
prompt) or an `OP_SERVICE_ACCOUNT_TOKEN` both work.

## Pools

A pool is one homogeneous group of runners sharing labels and a provider:

```yaml
pools:
  - name: macos
    labels: [self-hosted, macos, arm64]
    provider: process
    min: 0
    max: 3
    idle_timeout: 10m
    job_timeout: 6h
    process:
      template_dir: /Users/you/.arc/runner-template
```

`labels` is what runners register with, and what `runs-on` has to match — see
[Which pool gets a job](scaling.md#which-pool-gets-a-job). Provider-specific
settings live under a `docker:` or `process:` block; see
[Providers](providers.md).

## Which repos get watched

There is no account-wide "list queued jobs" endpoint, so arc walks each repo's
active workflow runs. Narrowing that set is the biggest lever on API cost:

```yaml
github:
  poll_interval: 15s
  repos:
    active_within: 720h        # skip repos with no push in 30 days
    include: ["service-*"]     # globs; an allowlist when non-empty
    exclude: ["archived-*"]
    refresh_interval: 10m
```

Every request is conditional on an ETag, and GitHub does not charge a `304`
against the rate limit — so quiet repos are effectively free to poll.
Personal-account runner listings are per repo and conditional for the same
reason; without that they would eat the hourly budget on their own.

`arc doctor` warns when your poll interval and repo count could outrun the
limit. Turning on [webhooks](webhooks.md) is the bigger lever: polling then
drops to a 2-minute safety net.

## Control API

```yaml
server:
  addr: 127.0.0.1:8730
  # token: ${ARC_API_TOKEN}
  # state_dir: defaults to a per-user config directory
```

This is what `arc status`, `arc scale`, `arc drain` and `arc logs` talk to. It
changes scaling and reads runner logs, so keep it on loopback unless you set
`server.token` and put TLS in front of it. `GET /metrics` serves Prometheus
gauges for live, busy, idle, desired, queued and rate-limit remaining.
