# Scaling

Each tick, for every pool:

```
desired = clamp(max(min, busy + queued), min, max)
```

`busy + queued` rather than just `queued` because runners are ephemeral: a
runner executing a job cannot also serve a queued one, so in-flight work and
waiting work both need capacity. `min` keeps warm runners so the first job of
the day doesn't pay full startup cost (org mode only — see
[Personal accounts](personal-accounts.md)).

Scale-down is conservative on purpose:

- Only **idle** runners are ever removed. A busy one is executing someone's
  build. GitHub itself refuses to deregister a busy runner (422), which is the
  backstop if the orchestrator's view is stale.
- A surplus runner must be continuously idle for `idle_timeout` before it goes,
  so a burst of jobs arriving seconds apart doesn't thrash create/destroy.
- Ephemeral runners exit on their own after one job anyway, so most scale-down
  is just arc reaping what already finished and topping back up to `min`.

## Which pool gets a job

A pool can serve a job when the pool's labels are a **superset** of the job's
`runs-on` labels — the same rule GitHub uses to route work. When several pools
qualify, the one with the fewest extra labels wins, so a plain
`runs-on: [self-hosted, linux]` job doesn't consume your scarce
`linux + gpu` machines. If the best-fit pool is at `max`, the job spills to
another eligible pool instead of waiting.

Jobs targeting GitHub-hosted images (`ubuntu-latest`, `macos-14`, …) are
ignored — GitHub runs those itself, and treating them as demand would spin up
local runners that never receive work.

`arc status` lists any queued job that matches no pool, so a typo in `runs-on`
shows up as a line of output rather than a job that hangs forever.

## Adjusting min and max

Live, without a restart, persisted across restarts:

```sh
arc scale linux -max 16        # raise the ceiling for a big migration
arc scale linux -min 3         # keep more warm (org mode only)
arc scale linux -reset         # back to the config file's values

arc drain windows              # stop creating; running jobs finish untouched
arc resume windows
```

## Several hosts, one account

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

One arc can also drive remote Docker daemons instead, which is the better shape
when the remote machines are headless Linux boxes:

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

See [Remote Docker daemons](providers.md#remote-docker-daemons).
