# Providers

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

Every runner is ephemeral: it takes exactly one job, unregisters itself, and is
deleted along with its entire filesystem. Both providers give you that promise
by different means:

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

## Runner images and templates

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

## Remote Docker daemons

A pool's daemon does not have to be local:

```yaml
docker:
  host: ssh://runner@gpu-box.local
```

`ssh://` uses your existing SSH keys and exposes no daemon port. `tcp://` with
client certificates works too; unauthenticated `tcp://` does not, deliberately.

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
