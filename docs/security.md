# Security notes

- The control API binds to loopback and changes scaling and reads runner logs.
  Set `server.token` if you must expose it, and put TLS in front of it.
- `docker_in_docker: true` gives every workflow root-equivalent control of that
  host's Docker daemon. It is off by default for that reason.
- Self-hosted runners and public repositories are a bad combination: anyone who
  can open a pull request can run code on your machine. The ephemeral model
  limits the blast radius but does not eliminate it, and on a personal account
  the machine is usually your own workstation. GitHub's guidance is to use
  self-hosted runners with private repos only.
- The runner template and image contain an unconfigured runner. Registration
  happens per job through a just-in-time config, so nothing on disk carries a
  credential that outlives the job.

## What's deliberately not here

- **Config hot-reload.** Limits change live with `arc scale`; anything else is a
  restart, which is cheap because runners are ephemeral and are left to finish.
- **Tart / VM runners.** The provider interface has room for it. macOS VMs would
  give per-job isolation the process provider can't, at a real cost in startup
  time and disk.
- **A coordination service.** Hosts sharing an account coordinate through
  GitHub's own state — runner names, hook paths, idle runners — so there is
  nothing extra to run, and nothing extra to lose.
