# Development

```sh
make build          # ./bin/arc
make install        # into $GOPATH/bin
make check          # fmt, vet, test
make test-unit      # skip the tests that need a Docker daemon
make crosscheck     # verify all five target platforms still compile
make release        # cross-compiled binaries in dist/
```

The Docker integration tests run against your local daemon, create real
containers under a test-only pool label, and clean up after themselves. They
skip automatically when no daemon is reachable.

`make crosscheck` matters more than it looks: the Windows and Unix process
providers are behind build tags, so it is the only thing that catches a break in
the one you are not developing on.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/arc` | The CLI: every command, the service installers, `doctor` |
| `internal/config` | Config schema, defaults, validation, the edit/wizard round-trip |
| `internal/ghapi` | GitHub client: repos, queued jobs, runners, JIT configs, hooks |
| `internal/orchestrator` | The reconcile loop, scaling decisions, job-to-pool matching |
| `internal/provider` | `dockerprov` and `processprov` — how a runner is created and destroyed |
| `internal/webhook` | Local listener, Cloudflare tunnel, hook registration |
| `internal/opconfig` | The 1Password vault item and its credentials cache |
| `internal/api` | The loopback control API `arc status` and friends talk to |
| `images/`, `scripts/` | Runner images (Linux, Windows) and template setup scripts |
| `deploy/` | Hand-written launchd / systemd / Windows service definitions |

## Smoke test

`.github/workflows/selfhosted-smoke.yml` is a manual `workflow_dispatch` job
targeting `[self-hosted, macos, arm64]`. Dispatch it and whichever arc host
serves that platform should pick it up within seconds.
