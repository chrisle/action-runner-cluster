# Operations

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

## Running as a service

`arc install` sets up the host's native background service and starts it:

| Platform | What it installs | Notes |
| --- | --- | --- |
| macOS | launchd agent `com.arc.runner` | An **agent**, not a daemon, or code signing fails. Log: `~/.arc/arc.log` |
| Linux | systemd unit `arc.service` | `sudo arc install`, runs as the invoking user. Logs: `journalctl -u arc` |
| Windows | logon scheduled task `arc` | A launcher loop restarts arc if it exits. Log: `%LOCALAPPDATA%\arc\arc.log` |

On Windows the task runs through `conhost.exe --headless`, which is what keeps
arc off the desktop: a console process started by the task scheduler is
otherwise handed off to Windows Terminal, which ignores `-WindowStyle Hidden`
and leaves a PowerShell window open for the whole session. A host installed
before this landed keeps the old action until you rerun `arc install`, which
rewrites the task in place.

`arc start` / `arc stop` drive it afterwards. Restarts never kill runners — they
finish their job and exit, and arc re-adopts what it finds. Hand-written unit
files are in `deploy/` if you'd rather manage the service yourself.

`-max N` sets the ceiling for the pool arc derives for this machine in
[1Password mode](configuration.md#zero-config-from-1password); with a config
file, the file's pools win.

## Checking on it

```sh
arc status                     # pools, counts, and why each is at its target
arc status -wide               # plus every individual runner instance
arc status -watch 2s           # refresh in place
arc logs macos <instance-id>   # tail one runner's output
```

`arc doctor` checks the config, the credentials and their scopes, repo
visibility, every Docker daemon, and every runner template — and reports all
failures at once rather than stopping at the first, so one pass is enough to fix
a fresh install.

`GET /metrics` on the [control API](configuration.md#control-api) serves
Prometheus gauges for live, busy, idle, desired, queued and rate-limit
remaining.

## Updating

```sh
arc update -check              # is there a newer release?
arc update                     # replace this binary with it
```

A development build ahead of the latest tag is left alone unless you pass
`-force`, so `arc update` never silently downgrades one.

## Removing a host

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
